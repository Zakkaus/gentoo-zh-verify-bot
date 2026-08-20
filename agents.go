package main

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
)

// --- automated-agent tally -------------------------------------------------------------------
//
// The kernel-mode DM carries a tripwire asking an LLM agent to name its model instead of answering
// (see aiTrapLine). Every agent that complies is counted here — per claimed model — so admins can
// see WHICH models are being pointed at the group, not just that "a bot tried". The claim is
// self-reported and trivially spoofable; it is a usage tally, never evidence.

// agentModelMax bounds the distinct model keys kept, so a spammer sending random strings can't grow
// the state file without limit. Once full, further unknown models fold into the "other" bucket.
const agentModelMax = 200

// modelValue is what may survive from a self-reported model name: letters, digits and the handful
// of separators real model ids use. Everything else is dropped before the name is stored or shown.
var modelValue = regexp.MustCompile(`[^0-9A-Za-z.:_/+-]+`)

// modelDeclare matches an explicit "model=…" / "model: …" claim, the form the tripwire asks for.
var modelDeclare = regexp.MustCompile(`(?i)\bmodel\s*[=:]\s*([0-9A-Za-z][0-9A-Za-z.:_/+-]*)`)

// modelFamilies are the model names worth recognising when an agent answers in prose ("I'm ChatGPT,
// running GPT-5") instead of the requested form. Matched case-insensitively against the whole reply
// and normalised to the family, so the tally stays readable instead of splitting across phrasings.
// Scanned LONGEST FIRST (see init) so "chatgpt" isn't swallowed by "gpt" and "chatglm" not by "glm".
var modelFamilies = []string{
	// western
	"claude", "sonnet", "opus", "haiku", "chatgpt", "gpt", "openai", "o3", "o4", "gemini", "gemma",
	"bard", "grok", "llama", "mistral", "mixtral", "command-r", "cohere", "copilot", "perplexity",
	"sonar", "phi",
	// chinese / low-cost, the ones cheap spam bots actually run
	"deepseek", "qwen", "tongyi", "kimi", "moonshot", "chatglm", "glm", "zhipu", "doubao", "hunyuan",
	"ernie", "wenxin", "spark", "minimax", "abab", "baichuan", "internlm", "yi", "step", "skywork",
	"telechat", "sensechat",
	// hosting layers an agent may name instead of a model
	"ollama", "openrouter", "groq", "together", "siliconflow",
}

// familyRe matches any of the names above as a WHOLE word, longest first (built in init). Word
// boundaries matter for the short ones: a bare Contains("yi") would tag half the alphabet.
var familyRe *regexp.Regexp

func init() {
	sort.Slice(modelFamilies, func(i, j int) bool {
		if len(modelFamilies[i]) != len(modelFamilies[j]) {
			return len(modelFamilies[i]) > len(modelFamilies[j]) // "chatgpt" must win over "gpt"
		}
		return modelFamilies[i] < modelFamilies[j]
	})
	familyRe = regexp.MustCompile(`(?i)\b(` + strings.Join(modelFamilies, "|") + `)\b`)
}

// claimedModel extracts the model an agent named in its reply: the requested "model=<id>" form
// first, then a recognised family name anywhere in the text, else "unknown". The result is
// sanitised and length-capped — it is untrusted input that ends up in a log line and a state file.
func claimedModel(text string) string {
	if m := modelDeclare.FindStringSubmatch(text); len(m) == 2 {
		return sanitizeModel(m[1])
	}
	if f := familyRe.FindString(text); f != "" {
		return strings.ToLower(f)
	}
	return "unknown"
}

// sanitizeModel strips anything that isn't part of a plausible model id and caps the length.
func sanitizeModel(s string) string {
	s = strings.ToLower(strings.TrimSpace(modelValue.ReplaceAllString(s, "")))
	if s == "" {
		return "unknown"
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// agentTally is the persisted count of tripped agents, keyed by claimed model.
type agentTally struct {
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

// recordAgent counts one tripped agent under its claimed model and persists the tally. Returns the
// model as recorded and the new total, for the log line and the admin alert.
func (v *Verifier) recordAgent(text string) (model string, total int) {
	model = claimedModel(text)
	v.agentMu.Lock()
	defer v.agentMu.Unlock()
	if v.agents.Counts == nil {
		v.agents.Counts = map[string]int{}
	}
	if _, known := v.agents.Counts[model]; !known && len(v.agents.Counts) >= agentModelMax {
		model = "other" // key cap reached: fold the long tail into one bucket
	}
	v.agents.Counts[model]++
	v.agents.Total++
	total = v.agents.Total
	tally := agentTally{Total: v.agents.Total, Counts: copyCounts(v.agents.Counts)}
	if v.agentPath != "" {
		writeJSONFile(v.agentPath, tally)
	}
	return model, total
}

// copyCounts copies a counter map so the snapshot written to disk can't race further increments.
// (Not named maps() — that would shadow the stdlib package for the whole main package.)
func copyCounts(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, n := range m {
		out[k] = n
	}
	return out
}

// loadAgents restores the tally at startup. A missing file is a first run; a corrupt one is backed
// up by loadJSONFile and we start from zero.
func (v *Verifier) loadAgents() {
	if v.agentPath == "" {
		return
	}
	var t agentTally
	if err := loadJSONFile(v.agentPath, &t); err != nil {
		return
	}
	v.agentMu.Lock()
	v.agents = t
	if v.agents.Counts == nil {
		v.agents.Counts = map[string]int{}
	}
	v.agentMu.Unlock()
	if t.Total > 0 {
		log.Printf("restored automated-agent tally: %d total across %d model(s)", t.Total, len(t.Counts))
	}
}

// agentStatsText renders the tally as one line, models ordered by count (ties alphabetical), or ""
// when nothing has ever been caught — so /stats stays quiet on a group that has never seen one.
func (v *Verifier) agentStatsText() string {
	v.agentMu.Lock()
	total := v.agents.Total
	counts := copyCounts(v.agents.Counts)
	v.agentMu.Unlock()
	if total == 0 {
		return ""
	}
	type kv struct {
		model string
		n     int
	}
	list := make([]kv, 0, len(counts))
	for m, n := range counts {
		list = append(list, kv{m, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].model < list[j].model
	})
	if len(list) > 6 { // keep the line short; the state file holds the full breakdown
		list = list[:6]
	}
	parts := make([]string, 0, len(list))
	for _, e := range list {
		parts = append(parts, fmt.Sprintf("%s %d", e.model, e.n))
	}
	return fmt.Sprintf("🤖 拦截 AI 代答:%d 次(%s)", total, strings.Join(parts, "、"))
}
