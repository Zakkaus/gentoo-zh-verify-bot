package main

import "math/rand"

// randomQuestion picks a random question from the group's pool (its own questions if
// configured, otherwise the global pool).
// (Go 1.20+ auto-seeds the global rand source, so no manual Seed is needed.)
func (c *Config) randomQuestion(gid int64) Question {
	qs := c.questions(gid)
	return qs[rand.Intn(len(qs))]
}

// shuffledQuestion returns the question text, its options in randomized order,
// and the index of the correct option within the shuffled slice. Shuffling
// prevents scripts from blindly clicking a fixed button position.
func shuffledQuestion(q Question) (text string, opts []string, correctIdx int) {
	order := rand.Perm(len(q.Options))
	opts = make([]string, len(order))
	for newPos, orig := range order {
		opts[newPos] = q.Options[orig]
		if orig == q.Answer {
			correctIdx = newPos
		}
	}
	return q.Q, opts, correctIdx
}
