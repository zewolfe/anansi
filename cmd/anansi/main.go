package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zewolfe/anansi/internal/config"
	"github.com/zewolfe/anansi/internal/k8s"
	"github.com/zewolfe/anansi/internal/orchestrator"
	"github.com/zewolfe/anansi/internal/output"
	"github.com/zewolfe/anansi/internal/render"
	"github.com/zewolfe/anansi/internal/sweep"
	tp "github.com/zewolfe/anansi/internal/throughput"
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
	Short: "Anansi",
	Long: `Anansi is a benchmarking harness for serverless LLM inference cold-start analysis.

	It systematically measures cold-start latency across runtimes, checkpoint formats,
	caching strategies, and model sizes on KServe/Knative/Kubernetes infrastructure.`,
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
	rootCmd.AddCommand(renderISVCsCmd)
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

		fmt.Printf("  Starting %d trials (%d configs + %d reps)\n",
			len(trials)*cfg.Experiment.Repetitions,
			len(trials),
			cfg.Experiment.Repetitions,
		)
		fmt.Printf("  Output: %s\n\n", outputDir)

		k8sClient, err := k8s.NewClient(cfg.Testbed.KubeContext)
		if err != nil {
			return fmt.Errorf("creating k8s client: %w", err)
		}

		orch := orchestrator.New(orchestrator.OrchestratorConfig{
			K8sClient: k8sClient,
			Namespace: cfg.Testbed.Namespace,
			OutputDir: outputDir,
			Verbose:   verbose,
		})
		defer orch.Close()

		results, err := orch.RunMatrix(cmd.Context(), trials, cfg.Experiment)
		if err != nil {
			return fmt.Errorf("matrix run: %w", err)
		}

		// Build and write summary
		summaryWriter := output.NewSummaryWriter(outputDir)
		expSummary := buildExperimentSummary(results)
		if err := summaryWriter.WriteSummary(expSummary); err != nil {
			return fmt.Errorf("writing summary: %w", err)
		}

		printFinalSummary(expSummary)
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

		k8sClient, err := k8s.NewClient(cfg.Testbed.KubeContext)
		if err != nil {
			return fmt.Errorf("creating k8s client: %w", err)
		}

		orch := orchestrator.New(orchestrator.OrchestratorConfig{
			K8sClient: k8sClient,
			Namespace: cfg.Testbed.Namespace,
			OutputDir: outputDir,
			Verbose:   true, // always verbose for decompose
		})
		defer orch.Close()

		results, err := orch.RunMatrix(cmd.Context(), trials, cfg.Experiment)
		if err != nil {
			return fmt.Errorf("decompose run: %w", err)
		}

		fmt.Println("━━━ Decomposition Validation ━━━")
		for _, r := range results {
			if !r.IsSuccess() {
				continue
			}
			errPct := r.DecompositionError()
			status := "✓"
			if errPct > 10 || errPct < -10 {
				status = "✗"
			}
			fmt.Printf("  %s rep %2d: Σcomponents vs TTFT error = %.1f%%\n",
				status, r.Rep, errPct)
		}
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

		fmt.Println("Anansi — Arrival Rate Sweep")
		fmt.Println("----")
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

		k8sClient, err := k8s.NewClient(cfg.Testbed.KubeContext)
		if err != nil {
			return fmt.Errorf("creating k8s client: %w", err)
		}

		orch := orchestrator.New(orchestrator.OrchestratorConfig{
			K8sClient: k8sClient,
			Namespace: cfg.Testbed.Namespace,
			OutputDir: outputDir,
			Verbose:   verbose,
		})
		defer orch.Close()

		fmt.Printf("  Running sweep across %d arrival rates...\n\n", len(cfg.Sweep.Rates))

		for _, rate := range cfg.Sweep.Rates {
			theoretical := sweep.TheoreticalPCold(rate, cfg.Sweep.IdleTimeoutSeconds)
			fmt.Printf("  Rate %.1f req/min — theoretical P(cold)=%.4f\n", rate, theoretical)

			gen := sweep.NewPoissonGenerator(rate, 42)
			schedule := gen.GenerateSchedule(cfg.Sweep.SweepDuration())
			fmt.Printf("    Generated %d arrivals over %d min\n", len(schedule), cfg.Sweep.DurationMinutes)
			fmt.Printf("    [sweep execution requires live cluster — schedule generated]\n\n")
		}

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

		fmt.Println("Anansi — Throughput Benchmark")
		fmt.Println("----")
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

		// Construct the inference URL
		// For throughput, we need the model to already be deployed and warm
		isvcName := fmt.Sprintf("%s-%s",
			cfg.Throughput.Config.Runtime, cfg.Throughput.Config.Model)
		inferenceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local/v1/completions",
			isvcName, cfg.Testbed.Namespace)

		runner := tp.NewRunner(verbose)

		// Warmup
		fmt.Printf("  Warming up with %d requests...\n", cfg.Throughput.WarmupRequests)
		if err := runner.Warmup(
			cmd.Context(), inferenceURL,
			cfg.Experiment.Prompt, cfg.Experiment.MaxTokens,
			cfg.Throughput.WarmupRequests,
		); err != nil {
			return fmt.Errorf("warmup failed: %w", err)
		}

		// Run each concurrency level
		for _, conc := range cfg.Throughput.Concurrency {
			fmt.Printf("\n  ━━━ Concurrency: %d ━━━\n", conc)

			results, err := runner.RunLevel(
				cmd.Context(), inferenceURL,
				cfg.Experiment.Prompt, cfg.Experiment.MaxTokens,
				conc, cfg.Throughput.ThroughputDuration(),
			)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				continue
			}

			agg := tp.Aggregate(results, conc,
				"throughput",
				cfg.Throughput.Config.Runtime,
				cfg.Throughput.Config.Format,
				cfg.Throughput.Config.Model,
			)

			fmt.Printf("    Requests:    %d\n", agg.TotalRequests)
			fmt.Printf("    Tokens/s:    %.1f\n", agg.TokensPerSec)
			fmt.Printf("    Median lat:  %.1f ms\n", agg.MedianLatMs)
			fmt.Printf("    P95 lat:     %.1f ms\n", agg.P95LatMs)
			fmt.Printf("    P99 lat:     %.1f ms\n", agg.P99LatMs)
			fmt.Printf("    Error rate:  %.2f%%\n", agg.ErrorRate*100)
		}

		fmt.Println()
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
		fmt.Println("Report Generator")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("  Input:  %s\n", outputDir)
		fmt.Println()

		rg := output.NewReportGenerator(outputDir)

		// Load all raw results
		grouped, err := rg.LoadAllResults()
		if err != nil {
			return fmt.Errorf("loading results: %w", err)
		}

		fmt.Printf("  Loaded %d configurations\n", len(grouped))

		// Build summary
		summary := &output.ExperimentSummary{
			TotalConfigs: len(grouped),
		}
		for _, group := range grouped {
			cs := output.BuildConfigSummary(group)
			summary.Configs = append(summary.Configs, cs)
			summary.TotalTrials += cs.Trials
			summary.TotalErrors += cs.Errors
		}

		// Write JSON summary
		sw := output.NewSummaryWriter(outputDir)
		if err := config.EnsureOutputDir(outputDir); err != nil {
			return err
		}
		if err := sw.WriteSummary(summary); err != nil {
			return fmt.Errorf("writing summary: %w", err)
		}
		fmt.Printf("  Written: %s/summary/summary.json\n", outputDir)

		// Write markdown report
		reportPath := fmt.Sprintf("%s/report/report.md", outputDir)
		if err := rg.GenerateMarkdownReport(summary, reportPath); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
		fmt.Printf("  Written: %s\n", reportPath)

		printFinalSummary(summary)
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

var renderISVCsCmd = &cobra.Command{
	Use:   "render-isvcs",
	Short: "Generate InferenceService manifests from a matrix yaml",
	Long:  `Generates KServe InferenceService Manifest files from a matrix yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			return err
		}
		renderer := render.NewRenderer(outputDir)

		return renderer.Render(cfg)
	},
}

func init() {
	// Flags for commands that need config + output
	for _, cmd := range []*cobra.Command{runCmd, decomposeCmd, sweepCmd, throughputCmd, renderISVCsCmd} {
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

	// Override reps if flag was set
	if reps > 0 {
		cfg.Experiment.Repetitions = reps
	}

	trials, err := config.ExpandMatrix(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("expanding matrix: %w", err)
	}

	return cfg, trials, nil
}

// buildExperimentSummary groups results by config and computes aggregate stats.
func buildExperimentSummary(results []config.TrialResult) *output.ExperimentSummary {
	// Group by config hash
	grouped := make(map[string][]config.TrialResult)
	for _, r := range results {
		grouped[r.ConfigHash] = append(grouped[r.ConfigHash], r)
	}

	summary := &output.ExperimentSummary{
		TotalConfigs: len(grouped),
		TotalTrials:  len(results),
	}

	for _, group := range grouped {
		cs := output.BuildConfigSummary(group)
		summary.Configs = append(summary.Configs, cs)
		summary.TotalErrors += cs.Errors
	}

	return summary
}

// printFinalSummary prints the final experiment summary to stdout.
func printFinalSummary(summary *output.ExperimentSummary) {
	fmt.Println("---Final Summary ---")
	fmt.Printf("  Configs:   %d\n", summary.TotalConfigs)
	fmt.Printf("  Trials:    %d\n", summary.TotalTrials)
	fmt.Printf("  Errors:    %d\n", summary.TotalErrors)
	fmt.Println()

	for _, cs := range summary.Configs {
		fmt.Printf("  %s\n", cs.Label)
		if cs.TTFT.N > 0 {
			fmt.Printf("    TTFT:  median=%.1fs  P95=%.1fs  CI95=[%.1f, %.1f]s  (n=%d)\n",
				cs.TTFT.Median/1000,
				cs.TTFT.P95/1000,
				cs.TTFT.CI95Lo/1000,
				cs.TTFT.CI95Hi/1000,
				cs.TTFT.N,
			)
		}
		if cs.Errors > 0 {
			fmt.Printf("    Errors: %d/%d trials\n", cs.Errors, cs.Trials)
		}
	}
	fmt.Println()
}
