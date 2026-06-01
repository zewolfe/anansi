package config

import (
	"math"
	"testing"
)

func floatEq(a, b float64) bool { return math.Abs(a-b) < 0.001 }

// realisticTimeline returns timestamps in the order Anansi expects in production:
//
//	t0 < t3 < t5 < t4 < t6 ≤ t7
//
// (i.e. model_loaded log line precedes /health 200 reponse, which precedes
// the inference response). Times are in nanoseconds.
func realisticTimeline() TrialResult {
	return TrialResult{
		T0: 1_000_000_000, // 1.0s — request sent
		T3: 3_000_000_000, // 3.0s — container started      (TOrch  = 2000ms)
		T5: 6_000_000_000, // 6.0s — model loaded log line  (TLoad  = 3000ms)
		T4: 8_000_000_000, // 8.0s — containers ready       (TInit  = 2000ms)
		T6: 9_000_000_000, // 9.0s — first token (= T7 here, no streaming)
		T7: 9_000_000_000, // 9.0s — response complete      (TServe = 1000ms)
	}
}

func TestComputeDerivedDurations_NormalOrdering(t *testing.T) {
	tr := realisticTimeline()
	tr.ComputeDerivedDurations()

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"TTFT_ms", tr.TTFT_ms, 8000},
		{"TOrch_ms", tr.TOrch_ms, 2000},
		{"TLoad_ms", tr.TLoad_ms, 3000},
		{"TInit_ms", tr.TInit_ms, 2000},
		{"TServe_ms", tr.TServe_ms, 1000},
	}
	for _, c := range checks {
		if !floatEq(c.got, c.want) {
			t.Errorf("%s = %.3f, want %.3f", c.name, c.got, c.want)
		}
	}
}

func TestComputeDerivedDurations_TelescopingHolds(t *testing.T) {
	tr := realisticTimeline()
	tr.ComputeDerivedDurations()

	sum := tr.TOrch_ms + tr.TLoad_ms + tr.TInit_ms + tr.TServe_ms
	if !floatEq(sum, tr.TTFT_ms) {
		t.Errorf("components must telescope to TTFT: sum=%.3f, TTFT=%.3f, diff=%.3f",
			sum, tr.TTFT_ms, sum-tr.TTFT_ms)
	}
}

func TestComputeDerivedDurations_T6Independent(t *testing.T) {
	// T6 is captured but not part of the four-component decomposition.
	// Changing T6 must not change any component.
	a := realisticTimeline()
	a.T6 = a.T7
	a.ComputeDerivedDurations()

	b := realisticTimeline()
	b.T6 = 8500000000 // somewhere between T4 and T7 (streaming-like)
	b.ComputeDerivedDurations()

	if a.TOrch_ms != b.TOrch_ms || a.TLoad_ms != b.TLoad_ms ||
		a.TInit_ms != b.TInit_ms || a.TServe_ms != b.TServe_ms {
		t.Errorf("components depend on T6 but shouldn't (T6 is reserved for future streaming split)")
	}
}

func TestComputeDerivedDurations_MissingT4(t *testing.T) {
	// If T4 isn't captured, TInit and TServe should be sentinel -1.
	// TOrch and TLoad don't depend on T4 and should still be valid.
	tr := realisticTimeline()
	tr.T4 = 0
	tr.ComputeDerivedDurations()

	if !floatEq(tr.TOrch_ms, 2000) {
		t.Errorf("TOrch_ms must not depend on T4: got %.3f, want 2000", tr.TOrch_ms)
	}
	if !floatEq(tr.TLoad_ms, 3000) {
		t.Errorf("TLoad_ms must not depend on T4: got %.3f, want 3000", tr.TLoad_ms)
	}
	if tr.TInit_ms != -1 {
		t.Errorf("TInit_ms must be -1 sentinel when T4 missing, got %.3f", tr.TInit_ms)
	}
	if tr.TServe_ms != -1 {
		t.Errorf("TServe_ms must be -1 sentinel when T4 missing, got %.3f", tr.TServe_ms)
	}
}

func TestComputeDerivedDurations_MissingT5(t *testing.T) {
	tr := realisticTimeline()
	tr.T5 = 0
	tr.ComputeDerivedDurations()

	if tr.TLoad_ms != -1 {
		t.Errorf("TLoad_ms must be -1 when T5 missing, got %.3f", tr.TLoad_ms)
	}
	if tr.TInit_ms != -1 {
		t.Errorf("TInit_ms must be -1 when T5 missing, got %.3f", tr.TInit_ms)
	}
}

func TestDecompositionValid_RealisticOrdering(t *testing.T) {
	tr := realisticTimeline()
	tr.ComputeDerivedDurations()
	if !tr.DecompositionValid() {
		t.Errorf("DecompositionValid() = false on realistic ordering; component values: "+
			"TOrch=%.1f TLoad=%.1f TInit=%.1f TServe=%.1f",
			tr.TOrch_ms, tr.TLoad_ms, tr.TInit_ms, tr.TServe_ms)
	}
}

func TestDecompositionValid_NegativeComponentFromBadOrdering(t *testing.T) {
	tr := TrialResult{
		T0: 1_000_000_000,
		T3: 3_000_000_000,
		T4: 4_000_000_000, //NAUGHTY: ContainersReady before model loaded
		T5: 6_000_000_000,
		T6: 7_000_000_000,
		T7: 7_500_000_000,
	}
	tr.ComputeDerivedDurations()

	if tr.TInit_ms >= 0 {
		t.Fatalf("test precondition: TInit_ms should be negative here (got %.3f); "+
			"check that TInit = T4-T5 in ComputeDerivedDurations", tr.TInit_ms)
	}
	if tr.DecompositionValid() {
		t.Errorf("DecompositionValid() = true with TInit_ms=%.3f; "+
			"must return false on any negative component", tr.TInit_ms)
	}
}

func TestDecompositionValid_MissingTimestamp(t *testing.T) {
	tr := realisticTimeline()
	tr.T4 = 0
	tr.ComputeDerivedDurations()
	if tr.DecompositionValid() {
		t.Error("DecompositionValid() = true with T4 missing; must return false on any sentinel component")
	}
}

func TestDecompositionError_ZeroOnPerfectTelescoping(t *testing.T) {
	tr := realisticTimeline()
	tr.ComputeDerivedDurations()

	got := tr.DecompositionError()
	if math.Abs(got) > 0.001 {
		t.Errorf("DecompositionError = %.6f%%, want ~0%% (components telescope to TTFT algebraically)", got)
	}
}

func TestDecompositionError_ZeroEvenOnWeirdOrdering(t *testing.T) {
	tr := TrialResult{
		T0: 1_000_000_000, T3: 3_000_000_000, T4: 4_000_000_000,
		T5: 6_000_000_000, T6: 7_000_000_000, T7: 7_500_000_000,
	}
	tr.ComputeDerivedDurations()

	got := tr.DecompositionError()
	if math.Abs(got) > 0.001 {
		t.Errorf("DecompositionError = %.6f%% on weird ordering, want ~0%%; "+
			"the new math telescopes for ANY timestamps. Component values: "+
			"TOrch=%.1f TLoad=%.1f TInit=%.1f TServe=%.1f sum=%.1f TTFT=%.1f",
			got, tr.TOrch_ms, tr.TLoad_ms, tr.TInit_ms, tr.TServe_ms,
			tr.TOrch_ms+tr.TLoad_ms+tr.TInit_ms+tr.TServe_ms, tr.TTFT_ms)
	}
}
