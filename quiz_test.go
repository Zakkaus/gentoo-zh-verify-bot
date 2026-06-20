package main

import "testing"

// TestShuffledQuestion verifies the quiz shuffler always reports the correct option's NEW
// index after shuffling — a wrong correctIdx would reject correct answers (or accept wrong
// ones), breaking verification. Run many times since the order is random.
func TestShuffledQuestion(t *testing.T) {
	q := Question{Q: "pick A", Options: []string{"A", "B", "C", "D"}, Answer: 0}
	for i := 0; i < 200; i++ {
		text, opts, correctIdx := shuffledQuestion(q)
		if text != q.Q {
			t.Fatalf("text changed: %q", text)
		}
		if len(opts) != len(q.Options) {
			t.Fatalf("option count changed: %d", len(opts))
		}
		if correctIdx < 0 || correctIdx >= len(opts) {
			t.Fatalf("correctIdx %d out of range", correctIdx)
		}
		// the option at correctIdx must be the original correct answer
		if opts[correctIdx] != q.Options[q.Answer] {
			t.Fatalf("correctIdx points at %q, want %q", opts[correctIdx], q.Options[q.Answer])
		}
	}
}
