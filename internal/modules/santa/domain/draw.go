package domain

import (
	cryptorand "crypto/rand"
	"math/rand/v2"
)

// A random cycle has exactly one incoming and one outgoing edge per person.
// With at least three people the generated draft has no reciprocal pairs.
func Draw(ids []int64) []Pair {
	order := append([]int64(nil), ids...)
	var seed [32]byte
	cryptorand.Read(seed[:])
	random := rand.New(rand.NewChaCha8(seed))
	random.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	result := make([]Pair, len(order))
	for i, id := range order {
		result[i] = Pair{id, order[(i+1)%len(order)]}
	}
	return result
}

func ValidatePairs(ids []int64, assignments []Pair) error {
	if len(ids) != len(assignments) {
		return ErrInvalid
	}
	allowed := make(map[int64]bool, len(ids))
	givers := make(map[int64]bool, len(ids))
	recipients := make(map[int64]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	for _, p := range assignments {
		if !allowed[p.GiverID] || !allowed[p.RecipientID] || p.GiverID == p.RecipientID || givers[p.GiverID] || recipients[p.RecipientID] {
			return ErrInvalid
		}
		givers[p.GiverID] = true
		recipients[p.RecipientID] = true
	}
	return nil
}
