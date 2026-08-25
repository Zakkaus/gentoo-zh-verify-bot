package main

import (
	"fmt"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"log"
	"regexp"
	"sort"
	"strings"
)

// Tallies self-reported models from the kernel challenge's agent tripwire.
// Claims are untrusted usage data, never evidence.

// Unknown models fold into "other" after this many distinct keys.
const agentModelMax = 200

// Only model-ID characters may reach logs or persisted state.
var modelValue = regexp.MustCompile(`[^0-9A-Za-z.:_/+-]+`)

// modelDeclare matches the explicit form requested by the tripwire.
var modelDeclare = regexp.MustCompile(`(?i)\bmodel\s*[=:]\s*([0-9A-Za-z][0-9A-Za-z.:_/+-]*)`)

// Prose replies are normalized to these families. Longest matches win.
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

// Whole-word matching prevents short names such as "yi" from matching inside other words.
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

// claimedModel extracts and sanitizes an explicit model ID or recognized family.
func claimedModel(text string) string {
	if m := modelDeclare.FindStringSubmatch(text); len(m) == 2 {
		return sanitizeModel(m[1])
	}
	if f := familyRe.FindString(text); f != "" {
		return strings.ToLower(f)
	}
	return "unknown"
}

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

// agentTally persists tripwire counts by claimed model.
type agentTally struct {
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

// recordAgent persists one tripwire result and returns its model and the new total.
func (v *Verifier) recordAgent(text string) (model string, total int) {
	model = claimedModel(text)
	// Snapshot under the store write lock before agentMu; reversing that order can deadlock other saves.
	count := func() any {
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
		return agentTally{Total: v.agents.Total, Counts: copyCounts(v.agents.Counts)}
	}
	if v.agentPath == "" {
		count() // no persistence configured: the in-memory tally still has to advance
		return model, total
	}
	_ = store.Save(v.agentPath, count)
	return model, total
}

// copyCounts isolates the persisted snapshot from later increments.
func copyCounts(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, n := range m {
		out[k] = n
	}
	return out
}

// Missing or corrupt state restores as an empty tally; unreadable state disables later writes.
func (v *Verifier) loadAgents() {
	if v.agentPath == "" {
		return
	}
	var t agentTally
	if err := store.Load(v.agentPath, &t); err != nil {
		if store.ReadFailed(err) {
			v.agentPath = ""
		}
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

// agentStatsText returns the six busiest models, or "" before the first catch.
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
