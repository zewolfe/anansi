// Package config defines the schema for Anansi benchmark configurations
// and provides matrix expansion, exclusion filtering, and config hashing.
package config

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// BenchConfig is the root configuration loaded from a YAML file.
type BenchConfig struct {
	Testbed    TestbedConfig     `yaml:"testbed"`
	Matrix     MatrixConfig      `yaml:"matrix"`
	Experiment ExperimentConfig  `yaml:"experiment"`
	Sweep      *SweepConfig      `yaml:"sweep,omitempty"`
	Throughput *ThroughputConfig `yaml:"throughput,omitempty"`
}

// TestbedConfig describes the Kubernetes cluster and storage backend.
type TestbedConfig struct {
	Namespace     string `yaml:"namespace"      validate:"required"`
	GPUDevice     string `yaml:"gpu_device"`
	MinIOEndpoint string `yaml:"minio_endpoint"`
	MinIOBucket   string `yaml:"minio_bucket"`
	KubeContext   string `yaml:"kube_context,omitempty"`
}

// MatrixConfig holds the axes of the experimental matrix.
type MatrixConfig struct {
	Runtimes   []RuntimeDef   `yaml:"runtimes"`
	Formats    []FormatDef    `yaml:"formats"`
	Models     []ModelDef     `yaml:"models"`
	Caching    []CachingDef   `yaml:"caching"`
	Scenarios  []ScenarioDef  `yaml:"scenarios"`
	Exclusions []ExclusionDef `yaml:"exclusions,omitempty"`
}

// ExperimentConfig holds per-trial execution parameters.
type ExperimentConfig struct {
	Repetitions     int    `yaml:"repetitions"`
	CooldownSeconds int    `yaml:"cooldown_seconds"`
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
	Prompt          string `yaml:"prompt"`
	MaxTokens       int    `yaml:"max_tokens"`
}

// Cooldown returns the cooldown as a time.Duration.
func (e ExperimentConfig) Cooldown() time.Duration {
	return time.Duration(e.CooldownSeconds) * time.Second
}

// Timeout returns the timeout as a time.Duration.
func (e ExperimentConfig) Timeout() time.Duration {
	return time.Duration(e.TimeoutSeconds) * time.Second
}

// RuntimeDef defines a serving runtime (e.g., llama.cpp default, vLLM optimised).
type RuntimeDef struct {
	Name         string            `yaml:"name"`
	Image        string            `yaml:"image"`
	RuntimeClass string            `yaml:"runtime_class,omitempty"`
	Loader       string            `yaml:"loader,omitempty"`      // "default" or "pipelined"
	LoaderArgs   string            `yaml:"loader_args,omitempty"` // e.g., "--n-streams 4 --prefault"
	Env          map[string]string `yaml:"env,omitempty"`
}

// FormatDef defines a checkpoint format and its storage path template.
type FormatDef struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"` // supports {model} placeholder
}

// ModelDef defines a model under test.
type ModelDef struct {
	Name   string  `yaml:"name"`
	SizeGB float64 `yaml:"size_gb"`
}

// CachingDef defines a caching strategy.
type CachingDef struct {
	Name            string `yaml:"name"`
	LocalModelCache bool   `yaml:"local_model_cache"`
}

// ScenarioDef defines a cold-start scenario (combination of caching + page cache state).
type ScenarioDef struct {
	Name          string `yaml:"name"`
	Caching       string `yaml:"caching"` // references CachingDef.Name
	DropPageCache bool   `yaml:"drop_page_cache"`
}

// ExclusionDef defines an invalid combination to skip (supports glob-style wildcards).
type ExclusionDef struct {
	Runtime  string `yaml:"runtime,omitempty"`
	Format   string `yaml:"format,omitempty"`
	Model    string `yaml:"model,omitempty"`
	Scenario string `yaml:"scenario,omitempty"`
}

// SweepConfig defines arrival-rate sweep parameters for M/G/1 validation.
type SweepConfig struct {
	Mode               string    `yaml:"mode"`  // "arrival_rate"
	Rates              []float64 `yaml:"rates"` // req/min
	DurationMinutes    int       `yaml:"duration_minutes"`
	IdleTimeoutSeconds int       `yaml:"idle_timeout_seconds"`
	Config             TrialSpec `yaml:"config"` // single config to sweep
}

// SweepDuration returns the per-rate duration.
func (s SweepConfig) SweepDuration() time.Duration {
	return time.Duration(s.DurationMinutes) * time.Minute
}

// ThroughputConfig defines sustained-load throughput measurement parameters.
type ThroughputConfig struct {
	Concurrency     []int     `yaml:"concurrency"` // e.g., [1, 5, 10, 20]
	DurationMinutes int       `yaml:"duration_minutes"`
	WarmupRequests  int       `yaml:"warmup_requests"` // requests to send before measurement
	Config          TrialSpec `yaml:"config"`
}

// ThroughputDuration returns the per-concurrency-level measurement duration.
func (t ThroughputConfig) ThroughputDuration() time.Duration {
	return time.Duration(t.DurationMinutes) * time.Minute
}

// TrialSpec identifies a single point in the configuration space (used by sweep/throughput).
type TrialSpec struct {
	Runtime  string `yaml:"runtime"`
	Format   string `yaml:"format"`
	Model    string `yaml:"model"`
	Scenario string `yaml:"scenario"`
}

// TrialConfig is a fully resolved, single experimental configuration
// ready for execution by the orchestrator.
type TrialConfig struct {
	Hash     string // SHA256 of canonical config tuple
	Runtime  RuntimeDef
	Format   FormatDef
	Model    ModelDef
	Scenario ScenarioDef
	Caching  CachingDef
}

// ConfigHash computes a deterministic SHA256 hash of the configuration tuple.
// Used for deduplication and result file naming.
func (tc TrialConfig) ConfigHash() string {
	canonical := fmt.Sprintf("%s|%s|%s|%s",
		tc.Runtime.Name,
		tc.Format.Name,
		tc.Model.Name,
		tc.Scenario.Name,
	)
	h := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("%x", h[:8]) // 16 hex chars — enough to avoid collisions in our space
}

// Label returns a human-readable label for logging and progress display.
func (tc TrialConfig) Label() string {
	return fmt.Sprintf("%s/%s/%s/%s", tc.Runtime.Name, tc.Format.Name, tc.Model.Name, tc.Scenario.Name)
}

// ModelPath resolves the {model} placeholder in the format path template.
func (tc TrialConfig) ModelPath() string {
	return strings.ReplaceAll(tc.Format.Path, "{model}", tc.Model.Name)
}

// TrialResult captures all timing data from a single cold-start trial.
type TrialResult struct {
	// Identity
	ConfigHash string `csv:"config_hash" json:"config_hash"`
	Runtime    string `csv:"runtime"     json:"runtime"`
	Format     string `csv:"format"      json:"format"`
	Model      string `csv:"model"       json:"model"`
	Scenario   string `csv:"scenario"    json:"scenario"`
	Rep        int    `csv:"rep"         json:"rep"`

	// Raw timestamps (Unix nanoseconds for precision)
	T0 int64 `csv:"t0_ns" json:"t0_ns"` // request sent
	T1 int64 `csv:"t1_ns" json:"t1_ns"` // activator received
	T2 int64 `csv:"t2_ns" json:"t2_ns"` // pod scheduled
	T3 int64 `csv:"t3_ns" json:"t3_ns"` // container started
	T4 int64 `csv:"t4_ns" json:"t4_ns"` // runtime ready
	T5 int64 `csv:"t5_ns" json:"t5_ns"` // model loaded
	T6 int64 `csv:"t6_ns" json:"t6_ns"` // first token generated
	T7 int64 `csv:"t7_ns" json:"t7_ns"` // response complete

	// Derived durations in milliseconds
	TTFT_ms   float64 `csv:"ttft_ms"      json:"ttft_ms"`
	TOrch_ms  float64 `csv:"t_orch_ms"    json:"t_orch_ms"`
	TServe_ms float64 `csv:"t_serve_ms"   json:"t_serve_ms"`
	TLoad_ms  float64 `csv:"t_load_ms"    json:"t_load_ms"`
	TInit_ms  float64 `csv:"t_init_ms"    json:"t_init_ms"`

	// Resource usage
	GPUMemMB int `csv:"gpu_mem_mb" json:"gpu_mem_mb"`

	// Error (empty string = success)
	Error string `csv:"error" json:"error,omitempty"`
}

// ComputeDerivedDurations fills in the _ms fields from raw timestamps.
func (tr *TrialResult) ComputeDerivedDurations() {
	nsToMs := func(start, end int64) float64 {
		if start == 0 || end == 0 {
			return -1 // sentinel: timestamp not captured
		}
		return float64(end-start) / 1e6
	}
	tr.TTFT_ms = nsToMs(tr.T0, tr.T7)
	tr.TOrch_ms = nsToMs(tr.T0, tr.T3)
	tr.TLoad_ms = nsToMs(tr.T3, tr.T5)
	tr.TInit_ms = nsToMs(tr.T5, tr.T4)
	tr.TServe_ms = nsToMs(tr.T4, tr.T7)
}

// IsSuccess returns true if the trial completed without error.
func (tr *TrialResult) IsSuccess() bool {
	return tr.Error == ""
}

// DecompositionError returns the percentage difference between the sum of
// components and end-to-end TTFT. Target: within 10%.
func (tr *TrialResult) DecompositionError() float64 {
	if tr.TTFT_ms <= 0 {
		return -1
	}
	sum := tr.TOrch_ms + tr.TServe_ms + tr.TLoad_ms + tr.TInit_ms
	return ((sum - tr.TTFT_ms) / tr.TTFT_ms) * 100.0
}

func (tr *TrialResult) DecompositionValid() bool {
	if tr.TTFT_ms <= 0 {
		return false
	}

	if tr.TOrch_ms < 0 || tr.TLoad_ms < 0 || tr.TInit_ms < 0 || tr.TServe_ms < 0 {
		return false
	}

	return true
}

// ThroughputResult captures metrics from a sustained-load throughput run.
type ThroughputResult struct {
	ConfigHash    string  `csv:"config_hash"       json:"config_hash"`
	Runtime       string  `csv:"runtime"           json:"runtime"`
	Format        string  `csv:"format"            json:"format"`
	Model         string  `csv:"model"             json:"model"`
	Concurrency   int     `csv:"concurrency"       json:"concurrency"`
	TotalRequests int     `csv:"total_requests"    json:"total_requests"`
	TotalTokens   int     `csv:"total_tokens"      json:"total_tokens"`
	DurationSec   float64 `csv:"duration_sec"      json:"duration_sec"`
	TokensPerSec  float64 `csv:"tokens_per_sec"    json:"tokens_per_sec"`
	MedianLatMs   float64 `csv:"median_lat_ms"     json:"median_lat_ms"`
	P95LatMs      float64 `csv:"p95_lat_ms"        json:"p95_lat_ms"`
	P99LatMs      float64 `csv:"p99_lat_ms"        json:"p99_lat_ms"`
	ErrorRate     float64 `csv:"error_rate"        json:"error_rate"`
}

// SweepResult captures empirical vs theoretical cold-start probability
// at a single arrival rate.
type SweepResult struct {
	ArrivalRate      float64 `csv:"arrival_rate_rpm"  json:"arrival_rate_rpm"`
	IdleTimeoutSec   int     `csv:"idle_timeout_sec"  json:"idle_timeout_sec"`
	TotalRequests    int     `csv:"total_requests"    json:"total_requests"`
	ColdStartCount   int     `csv:"cold_start_count"  json:"cold_start_count"`
	EmpiricalPCold   float64 `csv:"empirical_p_cold"  json:"empirical_p_cold"`
	TheoreticalPCold float64 `csv:"theoretical_p_cold" json:"theoretical_p_cold"`
	AbsoluteError    float64 `csv:"absolute_error"    json:"absolute_error"`
}
