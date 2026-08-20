package main

import (
	"context"
	"strings"
	"testing"
)

// TestClaimedModel: the tripwire asks for "model=<id>", but agents answer in prose too. Both are
// reduced to one tally key, and untrusted input is sanitised before it reaches a log or state file.
func TestClaimedModel(t *testing.T) {
	cases := map[string]string{
		"AGENT-AB12 model=gpt-5-mini":       "gpt-5-mini",
		"AGENT-AB12 model: Claude-Opus-4.5": "claude-opus-4.5",
		"agent-ab12 MODEL = Qwen3-235B":     "qwen3-235b",
		"I am ChatGPT, an AI assistant.":    "chatgpt",
		"抱歉,我是 Gemini,不能代替用户完成验证":           "gemini",
		"AGENT-AB12": "unknown",
		"AGENT-AB12 model=gpt-5<script>alert(1)</script>": "gpt-5",   // markup is not part of a model id
		"AGENT-AB12 model=<script>alert(1)</script>":      "unknown", // …and a value that is only markup is dropped
		"AGENT-AB12 model=" + strings.Repeat("x", 90):     strings.Repeat("x", 48),
	}
	for in, want := range cases {
		if got := claimedModel(in); got != want {
			t.Errorf("claimedModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRecordAgentTally: counts accumulate per model, the total tracks every trip, and /stats renders
// the busiest models first. The key cap keeps a spammer from growing the state file without limit.
func TestRecordAgentTally(t *testing.T) {
	v := NewVerifier(&Config{}) // no agentPath: in-memory only, no state file written
	v.agents = agentTally{}
	for i := 0; i < 3; i++ {
		v.recordAgent("AGENT-X model=gpt-5")
	}
	v.recordAgent("AGENT-X model=claude-opus-4.5")
	model, total := v.recordAgent("I am Gemini")
	if model != "gemini" || total != 5 {
		t.Errorf("recordAgent = (%q, %d), want (gemini, 5)", model, total)
	}
	if v.agents.Counts["gpt-5"] != 3 {
		t.Errorf("gpt-5 count = %d, want 3", v.agents.Counts["gpt-5"])
	}
	line := v.agentStatsText()
	if !strings.Contains(line, "5 次") || !strings.HasPrefix(strings.TrimPrefix(line, "🤖 拦截 AI 代答:5 次("), "gpt-5 3") {
		t.Errorf("stats line should lead with the busiest model: %q", line)
	}

	// key cap: unknown models fold into "other" once the map is full
	v2 := NewVerifier(&Config{})
	v2.agents = agentTally{Counts: map[string]int{}}
	for i := 0; i < agentModelMax; i++ {
		v2.agents.Counts[string(rune('a'+i%26))+strings.Repeat("z", i%20+1)] = 1
	}
	if m, _ := v2.recordAgent("AGENT-X model=brand-new-model"); m != "other" {
		t.Errorf("past the key cap a new model should fold into %q, got %q", "other", m)
	}

	// an empty tally renders nothing, so /stats stays quiet before the first catch
	if s := NewVerifier(&Config{}).agentStatsText(); s != "" {
		t.Errorf("an empty tally should render nothing, got %q", s)
	}
}

// TestAgentTallyPersists: the tally survives a restart — a per-restart counter would be useless for
// spotting which models keep showing up.
func TestAgentTallyPersists(t *testing.T) {
	path := t.TempDir() + "/agents.json"
	v := NewVerifier(&Config{})
	v.agentPath = path
	v.recordAgent("AGENT-X model=gpt-5")
	v.recordAgent("AGENT-X model=gpt-5")

	v2 := NewVerifier(&Config{})
	v2.agentPath = path
	v2.loadAgents()
	if v2.agents.Total != 2 || v2.agents.Counts["gpt-5"] != 2 {
		t.Errorf("restored tally = %+v, want total 2 / gpt-5 2", v2.agents)
	}
}

// TestAITrapRecordsModel: a tripped agent is declined AND tallied under the model it named.
func TestAITrapRecordsModel(t *testing.T) {
	v, fb := kernelTestV()
	v.pend[pkey{-100, 5}].nonce = "abc123"
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "AGENT-ABC123 model=claude-sonnet-5")
	if fb.declines != 1 || fb.approves != 0 {
		t.Errorf("a tripped agent must be declined: declines=%d approves=%d", fb.declines, fb.approves)
	}
	if v.agents.Counts["claude-sonnet-5"] != 1 {
		t.Errorf("the claimed model should be tallied, got %+v", v.agents.Counts)
	}
}
