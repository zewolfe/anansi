package output

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/zewolfe/anansi/internal/config"
)

// ConfigSummary holds aggregated statistics for a single configuration.
type ConfigSummary struct {
	ConfigHash string `json:"config_hash"`
	Runtime    string `json:"runtime"`
	Format     string `json:"format"`
	Model      string `json:"model"`
	Scenario   string `json:"scenario"`
	Label      string `json:"label"`
	Trials     int    `json:"trials"`
	Errors     int    `json:"errors"`

	TTFT   DurationStats `json:"ttft_ms"`
	TOrch  DurationStats `json:"t_orch_ms"`
	TServe DurationStats `json:"t_serve_ms"`
	TLoad  DurationStats `json:"t_load_ms"`
	TInit  DurationStats `json:"t_init_ms"`

	DecompErrorPct DurationStats `json:"decomp_error_pct"`
}

// DurationStats holds descriptive statistics for a duration metric.
type DurationStats struct {
	Median float64 `json:"median"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P5     float64 `json:"p5"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	CI95Lo float64 `json:"ci95_lo"`
	CI95Hi float64 `json:"ci95_hi"`
	N      int     `json:"n"`
}

// ExperimentSummary is the top-level summary of an entire experiment run.
type ExperimentSummary struct {
	TotalConfigs int              `json:"total_configs"`
	TotalTrials  int              `json:"total_trials"`
	TotalErrors  int              `json:"total_errors"`
	Configs      []ConfigSummary  `json:"configs"`
	Comparisons  []PairComparison `json:"comparisons,omitempty"`
}

// PairComparison holds Welch's t-test and Cohen's d for a pair of configs.
type PairComparison struct {
	ConfigA     string  `json:"config_a"`
	ConfigB     string  `json:"config_b"`
	Metric      string  `json:"metric"`
	MeanA       float64 `json:"mean_a"`
	MeanB       float64 `json:"mean_b"`
	DiffMs      float64 `json:"diff_ms"`
	WelchT      float64 `json:"welch_t"`
	PValue      float64 `json:"p_value"`
	CohensD     float64 `json:"cohens_d"`
	Significant bool    `json:"significant"` // p < 0.05
}

type SummaryWriter struct {
	baseDir string
}

func NewSummaryWriter(baseDir string) *SummaryWriter {
	return &SummaryWriter{
		baseDir: filepath.Join(baseDir, "summary"),
	}
}

func (sw *SummaryWriter) WriteSummary(summary *ExperimentSummary) error {
	path := filepath.Join(sw.baseDir, "summary.json")

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling summary: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing summary to %s: %w", path, err)
	}

	return nil
}

// BuildConfigSummary computes aggregate statistics for a set of trial results
// belonging to a single configuration.
func BuildConfigSummary(results []config.TrialResult) ConfigSummary {
	if len(results) == 0 {
		return ConfigSummary{}
	}

	first := results[0]
	summary := ConfigSummary{
		ConfigHash: first.ConfigHash,
		Runtime:    first.Runtime,
		Format:     first.Format,
		Model:      first.Model,
		Scenario:   first.Scenario,
		Label: fmt.Sprintf("%s/%s/%s/%s",
			first.Runtime, first.Format, first.Model, first.Scenario),
		Trials: len(results),
	}

	var ttfts, orchs, runtimes, loads, inits, decompErrs []float64
	for _, r := range results {
		if !r.IsSuccess() {
			summary.Errors++
			continue
		}
		if r.TTFT_ms > 0 {
			ttfts = append(ttfts, r.TTFT_ms)
		}
		if r.TOrch_ms > 0 {
			orchs = append(orchs, r.TOrch_ms)
		}
		if r.TServe_ms > 0 {
			runtimes = append(runtimes, r.TServe_ms)
		}
		if r.TLoad_ms > 0 {
			loads = append(loads, r.TLoad_ms)
		}
		if r.TInit_ms > 0 {
			inits = append(inits, r.TInit_ms)
		}
		de := r.DecompositionError()
		if de != -1 {
			decompErrs = append(decompErrs, de)
		}
	}

	summary.TTFT = computeStats(ttfts)
	summary.TOrch = computeStats(orchs)
	summary.TServe = computeStats(runtimes)
	summary.TLoad = computeStats(loads)
	summary.TInit = computeStats(inits)
	summary.DecompErrorPct = computeStats(decompErrs)

	return summary
}

func computeStats(values []float64) DurationStats {
	n := len(values)
	if n == 0 {
		return DurationStats{}
	}

	sorted := make([]float64, n)
	copy(sorted, values)
	sort.Float64s(sorted)

	stats := DurationStats{
		N:      n,
		Min:    sorted[0],
		Max:    sorted[n-1],
		Median: percentile(sorted, 50),
		P5:     percentile(sorted, 5),
		P95:    percentile(sorted, 95),
		P99:    percentile(sorted, 99),
	}

	// Mean
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	stats.Mean = sum / float64(n)

	// Stddev
	if n > 1 {
		sumSq := 0.0
		for _, v := range values {
			diff := v - stats.Mean
			sumSq += diff * diff
		}
		stats.StdDev = math.Sqrt(sumSq / float64(n-1))
	}

	stats.CI95Lo, stats.CI95Hi = bootstrapCI(sorted, 10000, 0.05)

	return stats
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := (p / 100.0) * float64(len(sorted)-1)
	lower := int(rank)
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := rank - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

// bootstrapCI computes bootstrap confidence interval for the median.
// Uses a simple seed-based PRNG for reproducibility.
func bootstrapCI(sorted []float64, nBoot int, alpha float64) (float64, float64) {
	n := len(sorted)
	if n < 3 {
		return sorted[0], sorted[n-1]
	}

	medians := make([]float64, nBoot)
	seed := uint64(42)
	for b := range nBoot {
		sum := 0.0
		sample := make([]float64, n)
		for i := range n {
			seed = seed*6364136223846793005 + 1442695040888963407
			idx := int(seed>>33) % n
			sample[i] = sorted[idx]
		}
		sort.Float64s(sample)
		_ = sum
		medians[b] = percentile(sample, 50)
	}

	sort.Float64s(medians)
	lo := percentile(medians, alpha/2*100)
	hi := percentile(medians, (1-alpha/2)*100)
	return lo, hi
}
