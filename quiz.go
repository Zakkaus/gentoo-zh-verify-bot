package main

import (
	"crypto/rand"
	"math/big"
)

// cryptoIntn returns a uniform random int in [0,n) backed by crypto/rand — the quiz pick and
// shuffle are an anti-automation control, so they use a cryptographic source rather than
// math/rand. Falls back to 0 on the practically-impossible RNG error (safe, just degenerate).
func cryptoIntn(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// randomQuestion picks a random question from the group's pool (its own questions if
// configured, otherwise the global pool).
func (c *Config) randomQuestion(gid int64) Question {
	qs := c.questions(gid)
	return qs[cryptoIntn(len(qs))]
}

// shuffledQuestion returns the question text, its options in randomized order, and the index of
// the correct option within the shuffled slice. Shuffling (Fisher–Yates, crypto/rand-backed)
// prevents scripts from blindly clicking a fixed button position.
func shuffledQuestion(q Question) (text string, opts []string, correctIdx int) {
	order := make([]int, len(q.Options))
	for i := range order {
		order[i] = i
	}
	for i := len(order) - 1; i > 0; i-- {
		j := cryptoIntn(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	opts = make([]string, len(order))
	for newPos, orig := range order {
		opts[newPos] = q.Options[orig]
		if orig == q.Answer {
			correctIdx = newPos
		}
	}
	return q.Q, opts, correctIdx
}
