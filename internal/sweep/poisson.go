// Package sweep implements arrival-rate sweep for M/G/1 queuing model validation.
package sweep

import (
	"math"
	"math/rand"
	"time"
)

type PoissonGenerator struct {
	rng  *rand.Rand
	rate float64 // arrivals per second
}

func NewPoissonGenerator(ratePerMinute float64, seed int64) *PoissonGenerator {
	return &PoissonGenerator{
		rng:  rand.New(rand.NewSource(seed)),
		rate: ratePerMinute / 60.0, // convert to per-second
	}
}

// NextInterval returns the next inter-arrival time.
// For a Poisson process, inter-arrival times are exponentially distributed:
func (pg *PoissonGenerator) NextInterval() time.Duration {
	u := pg.rng.Float64()
	for u == 0 {
		u = pg.rng.Float64()
	}
	intervalSec := -math.Log(u) / pg.rate
	return time.Duration(intervalSec * float64(time.Second))
}

// GenerateSchedule produces a list of arrival times relative to start
// for the given duration.
func (pg *PoissonGenerator) GenerateSchedule(duration time.Duration) []time.Duration {
	var schedule []time.Duration
	elapsed := time.Duration(0)

	for elapsed < duration {
		gap := pg.NextInterval()
		elapsed += gap
		if elapsed < duration {
			schedule = append(schedule, elapsed)
		}
	}

	return schedule
}

// TheoreticalPCold computes the theoretical cold-start probability
// for an M/G/1 queue with server vacations:
//
// where lamba = arrival rate (req/s) and t = idle timeout (seconds).
func TheoreticalPCold(ratePerMinute float64, idleTimeoutSeconds int) float64 {
	lambda := ratePerMinute / 60.0 // convert to per-second
	t := float64(idleTimeoutSeconds)
	return math.Exp(-lambda * t)
}

// ClassifyColdStart determines whether a request experienced a cold-start
// based on TTFT exceeding a threshold. The threshold is typically set as
// a multiple of the warm-state latency.
//
// Rule of thumb: if TTFT > warmLatencyMs * multiplier, it's cold.
func ClassifyColdStart(ttftMs float64, warmLatencyMs float64, multiplier float64) bool {
	if multiplier <= 0 {
		multiplier = 3.0
	}
	return ttftMs > warmLatencyMs*multiplier
}
