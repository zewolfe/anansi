package config

import (
	"os"
	"path/filepath"
	"testing"
)

func minimalConfig() BenchConfig {
	return BenchConfig{
		Testbed: TestbedConfig{
			Namespace: "kserve-bench",
		},
		Matrix: MatrixConfig{
			Runtimes: []RuntimeDef{
				{Name: "llamacpp-default", Image: "ghcr.io/test/llamacpp:latest", Loader: "default"},
				{Name: "llamacpp-pipelined", Image: "ghcr.io/test/llamacpp:latest", Loader: "pipelined"},
				{Name: "vllm-default", Image: "kserve/vllm:latest"},
				{Name: "vllm-faststart", Image: "kserve/vllm:latest"},
			},
			Formats: []FormatDef{
				{Name: "safetensors-fp16", Path: "s3://models/{model}/fp16/"},
				{Name: "gguf-q8", Path: "s3://models/{model}/q8_0.gguf"},
				{Name: "gguf-q4km", Path: "s3://models/{model}/q4_k_m.gguf"},
			},
			Models: []ModelDef{
				{Name: "phi3-mini", SizeGB: 7.6},
				{Name: "llama3-8b", SizeGB: 16.1},
			},
			Caching: []CachingDef{
				{Name: "remote", LocalModelCache: false},
				{Name: "lmc", LocalModelCache: true},
			},
			Scenarios: []ScenarioDef{
				{Name: "s1-remote-cold", Caching: "remote", DropPageCache: true},
				{Name: "s2-lmc-cold", Caching: "lmc", DropPageCache: true},
				{Name: "s3-lmc-warm", Caching: "lmc", DropPageCache: false},
			},
			Exclusions: []ExclusionDef{
				{Runtime: "vllm-*", Format: "gguf-*"},
				{Runtime: "llamacpp-*", Format: "safetensors-fp16"},
			},
		},
		Experiment: ExperimentConfig{
			Repetitions:     10,
			CooldownSeconds: 30,
			TimeoutSeconds:  300,
			Prompt:          "Hello",
			MaxTokens:       1,
		},
	}
}

// ---------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------

func TestValidateMinimalConfig(t *testing.T) {
	cfg := minimalConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidateMissingNamespace(t *testing.T) {
	cfg := minimalConfig()
	cfg.Testbed.Namespace = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing namespace")
	}
}

func TestValidateZeroRepetitions(t *testing.T) {
	cfg := minimalConfig()
	cfg.Experiment.Repetitions = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero repetitions")
	}
}

func TestValidateBadCachingReference(t *testing.T) {
	cfg := minimalConfig()
	cfg.Matrix.Scenarios = append(cfg.Matrix.Scenarios, ScenarioDef{
		Name: "bad-scenario", Caching: "nonexistent", DropPageCache: true,
	})
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for bad caching reference")
	}
}

func TestExpandMatrixCount(t *testing.T) {
	cfg := minimalConfig()
	trials, err := ExpandMatrix(&cfg)
	if err != nil {
		t.Fatalf("ExpandMatrix failed: %v", err)
	}

	// 4 runtimes × 3 formats × 2 models × 3 scenarios = 72
	// Exclusions: vllm-* × gguf-* removes 2 runtimes × 2 formats × 2 models × 3 scenarios = 24
	// Exclusions: llamacpp-* × safetensors-fp16 removes 2 runtimes × 1 format × 2 models × 3 scenarios = 12
	// Total excluded = 24 + 12 = 36
	// Valid = 72 - 36 = 36
	// But: vllm × safetensors = 2 × 1 × 2 × 3 = 12
	//      llamacpp × gguf = 2 × 2 × 2 × 3 = 24
	// Total valid = 12 + 24 = 36
	expected := 36
	if len(trials) != expected {
		t.Fatalf("expected %d configs, got %d", expected, len(trials))
	}
}

func TestExpandMatrixNoVLLMGGUF(t *testing.T) {
	cfg := minimalConfig()
	trials, err := ExpandMatrix(&cfg)
	if err != nil {
		t.Fatalf("ExpandMatrix failed: %v", err)
	}

	for _, tc := range trials {
		if tc.Runtime.Name == "vllm-default" && tc.Format.Name == "gguf-q8" {
			t.Fatal("exclusion should have removed vllm-default + gguf-q8")
		}
		if tc.Runtime.Name == "vllm-faststart" && tc.Format.Name == "gguf-q4km" {
			t.Fatal("exclusion should have removed vllm-faststart + gguf-q4km")
		}
		if tc.Runtime.Name == "llamacpp-default" && tc.Format.Name == "safetensors-fp16" {
			t.Fatal("exclusion should have removed llamacpp-default + safetensors-fp16")
		}
	}
}

func TestConfigHashDeterministic(t *testing.T) {
	cfg := minimalConfig()
	trials, err := ExpandMatrix(&cfg)
	if err != nil {
		t.Fatalf("ExpandMatrix failed: %v", err)
	}

	// Run expansion again — hashes should be identical
	trials2, err := ExpandMatrix(&cfg)
	if err != nil {
		t.Fatalf("second ExpandMatrix failed: %v", err)
	}

	if len(trials) != len(trials2) {
		t.Fatalf("different trial counts: %d vs %d", len(trials), len(trials2))
	}

	for i := range trials {
		if trials[i].Hash != trials2[i].Hash {
			t.Fatalf("hash mismatch at index %d: %s vs %s", i, trials[i].Hash, trials2[i].Hash)
		}
	}
}

func TestConfigHashUniqueness(t *testing.T) {
	cfg := minimalConfig()
	trials, err := ExpandMatrix(&cfg)
	if err != nil {
		t.Fatalf("ExpandMatrix failed: %v", err)
	}

	seen := make(map[string]string)
	for _, tc := range trials {
		if existing, ok := seen[tc.Hash]; ok {
			t.Fatalf("hash collision: %s and %s both hash to %s", existing, tc.Label(), tc.Hash)
		}
		seen[tc.Hash] = tc.Label()
	}
}

func TestModelPathResolution(t *testing.T) {
	tc := TrialConfig{
		Format: FormatDef{Name: "gguf-q4km", Path: "s3://models/{model}/q4_k_m.gguf"},
		Model:  ModelDef{Name: "phi3-mini"},
	}
	expected := "s3://models/phi3-mini/q4_k_m.gguf"
	if got := tc.ModelPath(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestTrialResultDecomposition(t *testing.T) {
	tr := TrialResult{
		T0: 1000000000, // 1s in ns
		T3: 3000000000, // 3s — T_orch = 2000ms
		T4: 4000000000, // 4s — T_runtime = 1000ms
		T5: 6000000000, // 6s — T_load = 2000ms
		T6: 7000000000, // 7s — T_init = 1000ms
		T7: 7500000000, // 7.5s — TTFT = 6500ms
	}
	tr.ComputeDerivedDurations()

	if tr.TTFT_ms != 6500 {
		t.Fatalf("expected TTFT=6500, got %f", tr.TTFT_ms)
	}
	if tr.TOrch_ms != 2000 {
		t.Fatalf("expected TOrch=2000, got %f", tr.TOrch_ms)
	}
	if tr.TLoad_ms != 2000 {
		t.Fatalf("expected TLoad=2000, got %f", tr.TLoad_ms)
	}

	// Decomposition error: sum = 2000+1000+2000+1000 = 6000 vs 6500 → -7.7%
	decomErr := tr.DecompositionError()
	if decomErr > -7 || decomErr < -8 {
		t.Fatalf("expected decomposition error ~-7.7%%, got %f%%", decomErr)
	}
}

func TestMatchesField(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"", "anything", true},                 // empty = match all
		{"vllm-default", "vllm-default", true}, // exact
		{"vllm-default", "vllm-faststart", false},
		{"vllm-*", "vllm-default", true}, // glob
		{"vllm-*", "vllm-faststart", true},
		{"vllm-*", "llamacpp-default", false},
		{"llamacpp-*", "llamacpp-pipelined", true},
	}

	for _, tt := range tests {
		got := matchesField(tt.pattern, tt.value)
		if got != tt.want {
			t.Errorf("matchesField(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.want)
		}
	}
}

func TestLoadConfigFromYAML(t *testing.T) {
	yamlContent := `
testbed:
  namespace: kserve-bench
  gpu_device: "nvidia.com/gpu"
  minio_endpoint: http://minio.default:9000
  minio_bucket: models

matrix:
  runtimes:
    - name: llamacpp-default
      image: ghcr.io/test/llamacpp:latest
      loader: default
  formats:
    - name: gguf-q4km
      path: "s3://models/{model}/q4_k_m.gguf"
  models:
    - name: phi3-mini
      size_gb: 7.6
  caching:
    - name: lmc
      local_model_cache: true
  scenarios:
    - name: s2-lmc-cold
      caching: lmc
      drop_page_cache: true

experiment:
  repetitions: 5
  cooldown_seconds: 15
  timeout_seconds: 120
  prompt: "Hello"
  max_tokens: 1
`
	// Write to temp file
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Testbed.Namespace != "kserve-bench" {
		t.Fatalf("expected namespace kserve-bench, got %s", cfg.Testbed.Namespace)
	}
	if cfg.Experiment.Repetitions != 5 {
		t.Fatalf("expected 5 reps, got %d", cfg.Experiment.Repetitions)
	}

	trials, err := ExpandMatrix(cfg)
	if err != nil {
		t.Fatalf("ExpandMatrix failed: %v", err)
	}
	// 1 runtime × 1 format × 1 model × 1 scenario = 1
	if len(trials) != 1 {
		t.Fatalf("expected 1 config, got %d", len(trials))
	}
}

func TestEnsureOutputDir(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "results")

	if err := EnsureOutputDir(base); err != nil {
		t.Fatalf("EnsureOutputDir failed: %v", err)
	}

	for _, sub := range []string{"raw", "summary", "report"} {
		p := filepath.Join(base, sub)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Fatalf("expected directory %s to exist", p)
		}
	}
}
