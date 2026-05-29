package sweep

import (
	"math"
	"testing"
	"time"
)

func TestPoissonGeneratorMeanInterval(t *testing.T) {
	// At 60 req/min = 1 req/s, mean inter-arrival time should be ~1s
	gen := NewPoissonGenerator(60.0, 42)

	n := 10000
	totalInterval := time.Duration(0)
	for range n {
		totalInterval += gen.NextInterval()
	}

	meanSec := totalInterval.Seconds() / float64(n)
	// Should be close to 1.0s (within 5%)
	if math.Abs(meanSec-1.0) > 0.05 {
		t.Fatalf("mean interval = %.3fs, want ~1.0s (rate=60/min)", meanSec)
	}
}

func TestPoissonGeneratorLowRate(t *testing.T) {
	// At 0.1 req/min, mean inter-arrival = 600s = 10min
	gen := NewPoissonGenerator(0.1, 42)

	n := 5000
	totalInterval := time.Duration(0)
	for range n {
		totalInterval += gen.NextInterval()
	}

	meanSec := totalInterval.Seconds() / float64(n)
	expected := 600.0
	// Within 10% for this sample size
	if math.Abs(meanSec-expected)/expected > 0.10 {
		t.Fatalf("mean interval = %.1fs, want ~%.1fs (rate=0.1/min)", meanSec, expected)
	}
}

func TestGenerateSchedule(t *testing.T) {
	gen := NewPoissonGenerator(10.0, 42) // 10 req/min
	duration := 5 * time.Minute

	schedule := gen.GenerateSchedule(duration)

	// Should get roughly 50 arrivals (10/min × 5 min)
	if len(schedule) < 30 || len(schedule) > 80 {
		t.Fatalf("expected ~50 arrivals, got %d", len(schedule))
	}

	// All arrivals should be within duration
	for i, arrival := range schedule {
		if arrival >= duration {
			t.Fatalf("arrival %d at %v exceeds duration %v", i, arrival, duration)
		}
	}

	// Arrivals should be monotonically increasing
	for i := 1; i < len(schedule); i++ {
		if schedule[i] <= schedule[i-1] {
			t.Fatalf("arrivals not monotonic at index %d: %v <= %v",
				i, schedule[i], schedule[i-1])
		}
	}
}

func TestGenerateScheduleDeterministic(t *testing.T) {
	gen1 := NewPoissonGenerator(5.0, 99)
	gen2 := NewPoissonGenerator(5.0, 99)

	s1 := gen1.GenerateSchedule(2 * time.Minute)
	s2 := gen2.GenerateSchedule(2 * time.Minute)

	if len(s1) != len(s2) {
		t.Fatalf("same seed produced different counts: %d vs %d", len(s1), len(s2))
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			t.Fatalf("same seed produced different arrival at %d: %v vs %v", i, s1[i], s2[i])
		}
	}
}

func TestTheoreticalPCold(t *testing.T) {
	tests := []struct {
		ratePerMin float64
		timeoutSec int
		wantApprox float64
	}{
		// High rate, long timeout, almost no cold starts
		{10.0, 30, math.Exp(-10.0 / 60.0 * 30.0)},
		// Low rate, short timeout, mostly cold starts
		{0.1, 30, math.Exp(-0.1 / 60.0 * 30.0)},
		// Medium rate
		{1.0, 30, math.Exp(-1.0 / 60.0 * 30.0)},
		// Edge: rate=0, all cold starts
		{0.0, 30, 1.0},
	}

	for _, tt := range tests {
		got := TheoreticalPCold(tt.ratePerMin, tt.timeoutSec)
		if math.Abs(got-tt.wantApprox) > 0.001 {
			t.Errorf("TheoreticalPCold(%.1f, %d) = %f, want %f",
				tt.ratePerMin, tt.timeoutSec, got, tt.wantApprox)
		}
	}
}

func TestClassifyColdStart(t *testing.T) {
	warmLatency := 100.0 // ms

	tests := []struct {
		ttft       float64
		multiplier float64
		wantCold   bool
	}{
		{50.0, 3.0, false},  // well under threshold
		{299.0, 3.0, false}, // just under 3x
		{301.0, 3.0, true},  // just over 3x
		{5000.0, 3.0, true}, // way over
		{250.0, 2.0, true},  // lower multiplier
		{150.0, 0, false},   // zero multiplier defaults to 3x = 300, so 150 < 300
	}

	for _, tt := range tests {
		got := ClassifyColdStart(tt.ttft, warmLatency, tt.multiplier)
		if got != tt.wantCold {
			t.Errorf("ClassifyColdStart(%.0f, %.0f, %.1f) = %v, want %v",
				tt.ttft, warmLatency, tt.multiplier, got, tt.wantCold)
		}
	}
}
