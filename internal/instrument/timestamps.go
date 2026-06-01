package instrument

import (
	"fmt"
	"time"

	"github.com/zewolfe/anansi/internal/config"
)

// Resolver combines timestamps from the event watcher, log parser,
// and the harness's own timing into a complete TrialResult.
type Resolver struct{}

func NewResolver() *Resolver {
	return &Resolver{}
}

// Resolve takes the harness-level timestamps (t0, t7), the K8s/log
// timestamps, and the trial config to produce a TrialResult.
//
// t0: time the HTTP request was sent (harness clock)
// t7: time the HTTP response was fully received (harness clock)
// firstTokenTime: time first token was received in the response stream (harness clock)
//   - if streaming was not used, set equal to t7
func (r *Resolver) Resolve(
	cfg config.TrialConfig,
	rep int,
	t0 time.Time,
	t7 time.Time,
	firstTokenTime time.Time,
	podTS *PodTimestamps,
) config.TrialResult {
	result := config.TrialResult{
		ConfigHash: cfg.ConfigHash(),
		Runtime:    cfg.Runtime.Name,
		Format:     cfg.Format.Name,
		Model:      cfg.Model.Name,
		Scenario:   cfg.Scenario.Name,
		Rep:        rep,
	}

	result.T0 = t0.UnixNano()
	result.T7 = t7.UnixNano()

	if !firstTokenTime.IsZero() {
		result.T6 = firstTokenTime.UnixNano()
	} else {
		// If we don't have a separate first-token time, use t7
		result.T6 = t7.UnixNano()
	}

	podTS.mu.Lock()
	defer podTS.mu.Unlock()

	if !podTS.ActivatorReceived.IsZero() {
		result.T1 = podTS.ActivatorReceived.UnixNano()
	}
	if !podTS.Scheduled.IsZero() {
		result.T2 = podTS.Scheduled.UnixNano()
	}
	if !podTS.ContainerStart.IsZero() {
		result.T3 = podTS.ContainerStart.UnixNano()
	}
	if !podTS.ContainersReady.IsZero() {
		result.T4 = podTS.ContainersReady.UnixNano()
	}
	if !podTS.ModelLoaded.IsZero() {
		result.T5 = podTS.ModelLoaded.UnixNano()
	}

	// Compute derived durations
	result.ComputeDerivedDurations()

	if !result.DecompositionValid() {
		result.Error = "Invalid decomposition"
	}

	return result
}

// ValidateDecomposition checks whether the sum of component times
// is within the specified tolerance of the end-to-end TTFT.
// Returns the error percentage and whether it's within tolerance.
func ValidateDecomposition(result *config.TrialResult, tolerancePct float64) (float64, bool) {
	errPct := result.DecompositionError()
	if errPct == -1 {
		return -1, false // missing data
	}
	absErr := errPct
	if absErr < 0 {
		absErr = -absErr
	}
	return errPct, absErr <= tolerancePct
}

// DiagnoseGaps identifies which timestamps are missing and reports
// potential causes. Useful for debugging instrumentation issues.
func DiagnoseGaps(result *config.TrialResult) []string {
	var issues []string

	if result.T0 == 0 {
		issues = append(issues, "CRITICAL: t0 (request sent) not recorded. Anasi harness error")
	}
	if result.T7 == 0 {
		issues = append(issues, "CRITICAL: t7 (response received) not recorded. Request may have timed out")
	}
	if result.T2 == 0 {
		issues = append(issues, "WARNING: t2 (pod scheduled) not captured. K8s event may have been missed")
	}
	if result.T3 == 0 {
		issues = append(issues, "WARNING: t3 (container started) not captured. Check event watcher selector")
	}
	if result.T4 == 0 {
		issues = append(issues, "WARNING: t4 (containers ready) not captured. Pod condition watch may have missed it")
	}
	if result.T5 == 0 {
		issues = append(issues, "WARNING: t5 (model loaded) not captured. Check log parser pattern for this runtime")
	}
	if result.T6 == 0 {
		issues = append(issues, "INFO: t6 (first token) not captured, defaulting to t7")
	}

	if result.T3 > 0 && result.T5 > 0 && result.T5 < result.T3 {
		issues = append(issues, "ORDERING: t5 (model loaded) precedes t3 (container started). Clock skew or logs captured the wrong line")
	}

	if result.T5 > 0 && result.T4 > 0 && result.T4 < result.T5 {
		issues = append(issues, "ORDERING: t4 (containers ready) precedes t5 (model loaded)")
	}

	if result.T4 > 0 && result.T7 > 0 && result.T7 < result.T4 {
		issues = append(issues, "ORDERING: t7 (response) precedes t4 (containers ready). Madness")
	}

	// Check temporal ordering
	timestamps := []struct {
		name  string
		value int64
	}{
		{"t0", result.T0},
		{"t2", result.T2},
		{"t3", result.T3},
		{"t4", result.T4},
		{"t5", result.T5},
		{"t6", result.T6},
		{"t7", result.T7},
	}

	for i := 1; i < len(timestamps); i++ {
		prev := timestamps[i-1]
		curr := timestamps[i]
		if prev.value != 0 && curr.value != 0 && curr.value < prev.value {
			issues = append(issues, fmt.Sprintf(
				"OOPS: %s (%d) is before %s (%d). Possible clock skew (delta: %dms)",
				curr.name, curr.value,
				prev.name, prev.value,
				(prev.value-curr.value)/1e6,
			))
		}
	}

	return issues
}

// SummaryLine returns a one-line summary of the trial result for progress display.
func SummaryLine(result *config.TrialResult) string {
	if !result.IsSuccess() {
		return fmt.Sprintf(" Failed on attempt %2d  ERROR: %s", result.Rep, result.Error)
	}

	decomp := ""
	if result.TOrch_ms > 0 && result.TLoad_ms > 0 {
		decomp = fmt.Sprintf("  [orch=%.1fs serve=%.1fs load=%.1fs init=%.1fs]",
			result.TOrch_ms/1000,
			result.TServe_ms/1000,
			result.TLoad_ms/1000,
			result.TInit_ms/1000,
		)
	}

	return fmt.Sprintf("  Successful attempt: %2d  TTFT=%.2fs%s",
		result.Rep,
		result.TTFT_ms/1000,
		decomp,
	)
}
