package main

import (
	"crypto/rand"
	"math/big"
)

// Challenge selection uses crypto/rand; failure degrades to deterministic index zero.
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

func (c *Config) randomQuestion(gid int64) Question {
	qs := c.questions(gid)
	return qs[cryptoIntn(len(qs))]
}

// Shuffling prevents fixed-position clicks while preserving the correct option's new index.
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
