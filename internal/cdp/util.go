package cdp

import (
	"math/rand/v2"
)

// Using math/rand/v2 (Go 1.22+) for a thread-safe, auto-seeded source.

func randInt64(max int64) int64 {
	if max <= 0 {
		return 0
	}
	return rand.Int64N(max)
}

func randFloat(min, max float64) float64 {
	return min + rand.Float64()*(max-min)
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
