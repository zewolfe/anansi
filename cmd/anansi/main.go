package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zewolfe/anansi/internal/config"
)

// Version is set at build time via ldflags
var Version = "dev"

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "anansi",
	Short: "Anansi — Cold-Start Bottleneck Hunter",
	Long: `Anansi is a benchmarking harness for serverless LLM inference cold-start analysis.

It systematically measures cold-start latency across runtimes, checkpoint formats,
caching strategies, and model sizes on KServe/Knative/Kubernetes infrastructure.

Named after Anansi the spider of Akan mythology — who tried to attend every feast
at once by tying ropes to each pot, only to discover that every path leads to
a bottleneck. This tool finds where the ropes pull tightest.`,
	Version: Version,
}

var (
	configPath string
	outputDir  string
	reps       int
	dryRun     bool
	verbose    bool
)

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(decomposeCmd)
	rootCmd.AddCommand(sweepCmd)
	rootCmd.AddCommand(throughputCmd)
	rootCmd.AddCommand(reportCmd)
	rootCmd.AddCommand(validateCmd)
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Execute the full cold-start evaluation matrix",
	Long: `Expands the configuration matrix (runtimes × formats × models × scenarios),
filters exclusions, and executes each valid configuration for the specified
number of repetitions. Outputs raw timing data and statistical summaries.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, trials, err := loadAndExpand(configPath)
		if err != nil {
			return err
		}

		config.PrintMatrixSummary(trials)

		if dryRun {
			fmt.Printf("  Dry run: would execute %d trials (%d configs × %d reps)\n\n",
				len(trials)*cfg.Experiment.Repetitions,
				len(trials),
				cfg.Experiment.Repetitions,
			)
			return nil
		}

		if err := config.EnsureOutputDir(outputDir); err != nil {
			return err
		}

		fmt.Printf("  Starting %d trials (%d configs × %d reps)\n",
			len(trials)*cfg.Experiment.Repetitions,
			len(trials),
			cfg.Experiment.Repetitions,
		)
		fmt.Printf("  Output: %s\n\n", outputDir)

		// TODO: wire up orchestrator
		fmt.Println("  [orchestrator not yet implemented — module 3]")
		return nil
	},
}

var decomposeCmd = &cobra.Command{
	Use:   "decompose",
	Short: "Detailed per-component cold-start decomposition",
	Long: `Runs a single configuration with high repetition count and captures
fine-grained per-component timing (orchestration, runtime init, model loading,
initialisation, warm-up). Validates that components sum to within 10% of TTFT.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, trials, err := loadAndExpand(configPath)
		if err != nil {
			return err
		}

		if len(trials) > 1 {
			fmt.Printf("  Warning: decompose mode works best with a single config.\n")
			fmt.Printf("  Got %d configs; will decompose each.\n\n", len(trials))
		}

		config.PrintMatrixSummary(trials)

		if dryRun {
			fmt.Printf("  Dry run: would execute %d decomposition trials\n\n",
				len(trials)*cfg.Experiment.Repetitions)
			return nil
		}

		if err := config.EnsureOutputDir(outputDir); err != nil {
			return err
		}

		// TODO: wire up orchestrator in decompose mode
		fmt.Println("  [decompose orchestrator not yet implemented — module 3]")
		return nil
	},
}

var sweepCmd = &cobra.Command{
	Use:   "sweep",
	Short: "Arrival rate sweep for queuing model validation",
	Long: `Generates Poisson arrivals at specified rates, records cold-start vs warm
responses, and compares empirical P(cold-start) against the M/G/1 theoretical
prediction P = e^(-λτ).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadAndExpand(configPath)
		if err != nil {
			return err
		}

		if cfg.Sweep == nil {
			return fmt.Errorf("sweep config section is required for sweep mode")
		}

		fmt.Printf("\n🕷  Anansi — Arrival Rate Sweep\n")
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("  Rates:          %v req/min\n", cfg.Sweep.Rates)
		fmt.Printf("  Duration/rate:  %d min\n", cfg.Sweep.DurationMinutes)
		fmt.Printf("  Idle timeout:   %d s\n", cfg.Sweep.IdleTimeoutSeconds)
		fmt.Printf("  Config:         %s/%s/%s/%s\n\n",
			cfg.Sweep.Config.Runtime, cfg.Sweep.Config.Format,
			cfg.Sweep.Config.Model, cfg.Sweep.Config.Scenario)

		if dryRun {
			fmt.Println("  Dry run: sweep not executed")
			return nil
		}

		if err := config.EnsureOutputDir(outputDir); err != nil {
			return err
		}

		// TODO: wire up sweep module
		fmt.Println("  [sweep module not yet implemented — module 6]")
		return nil
	},
}

var throughputCmd = &cobra.Command{
	Use:   "throughput",
	Short: "Sustained-load throughput benchmark",
	Long: `Measures warm-state throughput (tokens/s) and latency percentiles
under concurrent load at specified concurrency levels.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadAndExpand(configPath)
		if err != nil {
			return err
		}

		if cfg.Throughput == nil {
			return fmt.Errorf("throughput config section is required for throughput mode")
		}

		fmt.Printf("\n🕷  Anansi — Throughput Benchmark\n")
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("  Concurrency:    %v\n", cfg.Throughput.Concurrency)
		fmt.Printf("  Duration/level: %d min\n", cfg.Throughput.DurationMinutes)
		fmt.Printf("  Warmup:         %d requests\n", cfg.Throughput.WarmupRequests)
		fmt.Printf("  Config:         %s/%s/%s/%s\n\n",
			cfg.Throughput.Config.Runtime, cfg.Throughput.Config.Format,
			cfg.Throughput.Config.Model, cfg.Throughput.Config.Scenario)

		if dryRun {
			fmt.Println("  Dry run: throughput benchmark not executed")
			return nil
		}

		if err := config.EnsureOutputDir(outputDir); err != nil {
			return err
		}

		// TODO: wire up throughput module
		fmt.Println("  [throughput module not yet implemented — module 7]")
		return nil
	},
}

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate statistical report from raw results",
	Long: `Reads raw trial data, computes descriptive statistics (median, P95, P99, CI),
runs pairwise comparisons (Welch's t-test, Cohen's d), validates decomposition
accuracy, and outputs summary JSON and markdown report.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("\n🕷  Anansi — Report Generator\n")
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("  Input:  %s\n", outputDir)
		fmt.Println()

		// TODO: wire up stats + report module
		fmt.Println("  [report module not yet implemented — module 5]")
		return nil
	},
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate a configuration file and print the expanded matrix",
	Long:  `Loads, validates, and expands the configuration matrix without executing any trials.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, trials, err := loadAndExpand(configPath)
		if err != nil {
			return err
		}
		config.PrintMatrixSummary(trials)
		fmt.Println("Configuration is valid")
		return nil
	},
}

func init() {
	// Flags for commands that need config + output
	for _, cmd := range []*cobra.Command{runCmd, decomposeCmd, sweepCmd, throughputCmd} {
		cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config file (required)")
		cmd.Flags().StringVarP(&outputDir, "output", "o", "results", "Output directory")
		cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and print plan without executing")
		cmd.MarkFlagRequired("config")
	}

	for _, cmd := range []*cobra.Command{runCmd, decomposeCmd} {
		cmd.Flags().IntVar(&reps, "reps", 0, "Override repetitions from config file")
	}

	reportCmd.Flags().StringVarP(&outputDir, "input", "i", "results", "Input results directory")

	validateCmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config file (required)")
	validateCmd.MarkFlagRequired("config")
}

func loadAndExpand(path string) (*config.BenchConfig, []config.TrialConfig, error) {
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}

	if reps > 0 {
		cfg.Experiment.Repetitions = reps
	}

	trials, err := config.ExpandMatrix(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("expanding matrix: %w", err)
	}

	return cfg, trials, nil
}
