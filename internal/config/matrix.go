package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadConfig reads and validates a BenchConfig from a YAML file.
func LoadConfig(path string) (*BenchConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg BenchConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config %s: %w", path, err)
	}

	return &cfg, nil
}

// Validate checks the config for required fields and logical consistency.
func (cfg *BenchConfig) Validate() error {
	if cfg.Testbed.Namespace == "" {
		return fmt.Errorf("testbed.namespace is required")
	}
	if len(cfg.Matrix.Runtimes) == 0 {
		return fmt.Errorf("at least one runtime is required")
	}
	if len(cfg.Matrix.Formats) == 0 {
		return fmt.Errorf("at least one format is required")
	}
	if len(cfg.Matrix.Models) == 0 {
		return fmt.Errorf("at least one model is required")
	}
	if len(cfg.Matrix.Scenarios) == 0 {
		return fmt.Errorf("at least one scenario is required")
	}
	if cfg.Experiment.Repetitions < 1 {
		return fmt.Errorf("repetitions must be >= 1, got %d", cfg.Experiment.Repetitions)
	}
	if cfg.Experiment.TimeoutSeconds < 1 {
		return fmt.Errorf("timeout_seconds must be >= 1")
	}
	if cfg.Experiment.Prompt == "" {
		cfg.Experiment.Prompt = "Hello"
	}
	if cfg.Experiment.MaxTokens < 1 {
		cfg.Experiment.MaxTokens = 1
	}

	// Validate scenario caching references
	cachingNames := make(map[string]bool)
	for _, c := range cfg.Matrix.Caching {
		cachingNames[c.Name] = true
	}
	for _, s := range cfg.Matrix.Scenarios {
		if s.Caching != "" && !cachingNames[s.Caching] {
			return fmt.Errorf("scenario %q references unknown caching %q", s.Name, s.Caching)
		}
	}

	return nil
}

// ExpandMatrix produces the Cartesian product of all matrix axes,
// filters out excluded combinations, resolves caching references,
// and computes config hashes. Returns the list of valid TrialConfigs.
func ExpandMatrix(cfg *BenchConfig) ([]TrialConfig, error) {
	// Build caching lookup
	cachingMap := make(map[string]CachingDef)
	for _, c := range cfg.Matrix.Caching {
		cachingMap[c.Name] = c
	}

	var trials []TrialConfig

	for _, rt := range cfg.Matrix.Runtimes {
		for _, frmt := range cfg.Matrix.Formats {
			for _, model := range cfg.Matrix.Models {
				for _, scenario := range cfg.Matrix.Scenarios {
					// Check exclusions
					if isExcluded(rt, frmt, model, scenario, cfg.Matrix.Exclusions) {
						continue
					}

					// Resolve caching reference
					caching, ok := cachingMap[scenario.Caching]
					if !ok && scenario.Caching != "" {
						return nil, fmt.Errorf(
							"scenario %v references unknown caching %v",
							scenario.Name, scenario.Caching,
						)
					}

					tc := TrialConfig{
						Runtime:  rt,
						Format:   frmt,
						Model:    model,
						Scenario: scenario,
						Caching:  caching,
					}
					tc.Hash = tc.ConfigHash()
					trials = append(trials, tc)
				}
			}
		}
	}

	if len(trials) == 0 {
		return nil, fmt.Errorf("matrix expansion produced zero valid configurations (check exclusions)")
	}

	return trials, nil
}

// isExcluded checks whether a combination matches any exclusion rule.
// Supports glob-style wildcards: "vllm-*" matches "vllm-default", "vllm-faststart".
func isExcluded(rt RuntimeDef, f FormatDef, m ModelDef, s ScenarioDef, exclusions []ExclusionDef) bool {
	for _, ex := range exclusions {
		if matchesField(ex.Runtime, rt.Name) &&
			matchesField(ex.Format, f.Name) &&
			matchesField(ex.Model, m.Name) &&
			matchesField(ex.Scenario, s.Name) {
			return true
		}
	}
	return false
}

// matchesField checks whether a value matches a pattern.
// Empty pattern matches everything. Supports trailing "*" glob.
func matchesField(pattern, value string) bool {
	if pattern == "" {
		return true // empty = wildcard (match all)
	}
	if pattern == value {
		return true
	}
	// Support trailing glob: "vllm-*" matches "vllm-default"
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(value, prefix)
	}
	return false
}

// EnsureOutputDir creates the output directory structure:
//
//	output/
//	├── raw/       (per-trial CSV files)
//	├── summary/   (aggregated JSON)
//	└── report/    (markdown report)
func EnsureOutputDir(base string) error {
	dirs := []string{
		filepath.Join(base, "raw"),
		filepath.Join(base, "summary"),
		filepath.Join(base, "report"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating output directory %s: %w", d, err)
		}
	}
	return nil
}

func PrintMatrixSummary(trials []TrialConfig) {
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("  Matrix expansion: %d valid configurations\n\n", len(trials))

	// Count unique values per axis
	runtimes := uniqueValues(trials, func(t TrialConfig) string { return t.Runtime.Name })
	formats := uniqueValues(trials, func(t TrialConfig) string { return t.Format.Name })
	models := uniqueValues(trials, func(t TrialConfig) string { return t.Model.Name })
	scenarios := uniqueValues(trials, func(t TrialConfig) string { return t.Scenario.Name })

	fmt.Printf("  Runtimes:  %s\n", strings.Join(runtimes, ", "))
	fmt.Printf("  Formats:   %s\n", strings.Join(formats, ", "))
	fmt.Printf("  Models:    %s\n", strings.Join(models, ", "))
	fmt.Printf("  Scenarios: %s\n", strings.Join(scenarios, ", "))
	fmt.Println()

	for i, t := range trials {
		fmt.Printf("  [%3d] %s  (hash: %s)\n", i+1, t.Label(), t.Hash[:12])
	}
	fmt.Println()
}

func uniqueValues(trials []TrialConfig, extract func(TrialConfig) string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, t := range trials {
		v := extract(t)
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}
