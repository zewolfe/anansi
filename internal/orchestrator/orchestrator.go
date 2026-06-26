// Package orchestrator coordinates the end-to-end trial lifecycle:
// prepare → trigger → collect → record → teardown.
package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/zewolfe/anansi/internal/cachedrop"
	"github.com/zewolfe/anansi/internal/config"
	"github.com/zewolfe/anansi/internal/instrument"
	"github.com/zewolfe/anansi/internal/k8s"
	"github.com/zewolfe/anansi/internal/output"
	"github.com/zewolfe/anansi/internal/render"
)

type Orchestrator struct {
	k8sClient      kubernetes.Interface
	dynamicClient  dynamic.Interface
	namespace      string
	podInformer    *instrument.PodInformer
	eventWatcher   *instrument.EventWatcher
	logParser      *instrument.LogParser
	resolver       *instrument.Resolver
	cacheDropper   *cachedrop.Client
	csvWriter      *output.CSVWriter
	endpointConfig *render.EndpointConfig
	httpClient     *http.Client
	verbose        bool
}

type OrchestratorConfig struct {
	K8sClient      kubernetes.Interface
	DynamicClient  dynamic.Interface
	Namespace      string
	OutputDir      string
	EndpointConfig render.EndpointConfig
	Verbose        bool
}

// New creates a fully wired Orchestrator.
func New(cfg OrchestratorConfig) *Orchestrator {
	return &Orchestrator{
		k8sClient:      cfg.K8sClient,
		dynamicClient:  cfg.DynamicClient,
		namespace:      cfg.Namespace,
		podInformer:    instrument.NewPodInformer(cfg.K8sClient, cfg.Namespace),
		eventWatcher:   instrument.NewEventWatcher(cfg.K8sClient, cfg.Namespace),
		logParser:      instrument.NewLogParser(cfg.K8sClient, cfg.Namespace),
		resolver:       instrument.NewResolver(),
		cacheDropper:   cachedrop.NewClient(cfg.K8sClient, cfg.Namespace),
		csvWriter:      output.NewCSVWriter(cfg.OutputDir),
		endpointConfig: &cfg.EndpointConfig,
		httpClient:     &http.Client{Timeout: 5 * time.Minute},
		verbose:        cfg.Verbose,
	}
}

// RunMatrix executes the full experimental matrix.
func (o *Orchestrator) RunMatrix(
	ctx context.Context,
	trials []config.TrialConfig,
	experiment config.ExperimentConfig,
) ([]config.TrialResult, error) {
	defer o.csvWriter.Close()

	totalTrials := len(trials) * experiment.Repetitions
	var allResults []config.TrialResult
	trialNum := 0

	for i, trial := range trials {
		fmt.Printf("\n--- Config %d/%d: %s ---\n", i+1, len(trials), trial.Label())

		for rep := 1; rep <= experiment.Repetitions; rep++ {
			trialNum++
			fmt.Printf("  Trial %d/%d (rep %d/%d)...\n",
				trialNum, totalTrials, rep, experiment.Repetitions)

			result, err := o.RunTrial(ctx, trial, rep, experiment)
			if err != nil {
				// Record the error as a failed trial, don't abort the matrix
				result = &config.TrialResult{
					ConfigHash: trial.ConfigHash(),
					Runtime:    trial.Runtime.Name,
					Format:     trial.Format.Name,
					Model:      trial.Model.Name,
					Scenario:   trial.Scenario.Name,
					Rep:        rep,
					T0:         time.Now().UnixNano(),
					Error:      err.Error(),
				}
				fmt.Printf("  ✗ rep %2d  ERROR: %v\n", rep, err)
			} else {
				fmt.Println(instrument.SummaryLine(result))
			}

			// Write to CSV regardless of success/failure
			if writeErr := o.csvWriter.WriteResult(result); writeErr != nil {
				fmt.Printf("  WARNING: failed to write CSV: %v\n", writeErr)
			}

			allResults = append(allResults, *result)

			// Cooldown between repetitions
			if rep < experiment.Repetitions {
				if o.verbose {
					fmt.Printf("  Cooling down %ds...\n", experiment.CooldownSeconds)
				}
				select {
				case <-ctx.Done():
					return allResults, ctx.Err()
				case <-time.After(experiment.Cooldown()):
				}
			}
		}

		// Extra cooldown between configurations
		if i < len(trials)-1 {
			select {
			case <-ctx.Done():
				return allResults, ctx.Err()
			case <-time.After(experiment.Cooldown()):
			}
		}
	}

	return allResults, nil
}

// RunTrial executes a single cold-start trial:
// prepare -> trigger -> collect -> teardown.
func (o *Orchestrator) RunTrial(
	ctx context.Context,
	trial config.TrialConfig,
	rep int,
	experiment config.ExperimentConfig,
) (*config.TrialResult, error) {
	trialCtx, cancel := context.WithTimeout(ctx, experiment.Timeout())
	defer cancel()

	// Derive the label selector for this trial's InferenceService
	labelSelector := inferenceServiceSelector(trial)
	runtimeType := detectRuntimeType(trial)

	// ─── PREPARE ─────────────────────────────────────────────
	if err := o.prepare(trialCtx, trial); err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}

	// ─── START COLLECTORS (before trigger) ───────────────────
	podTS := &instrument.PodTimestamps{}

	// Event watcher in background
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- o.eventWatcher.WatchPodEvents(
			trialCtx, labelSelector, podTS, experiment.Timeout())
	}()

	podInformerDone := make(chan error, 1)
	go func() {
		podInformerDone <- o.podInformer.WatchPods(
			trialCtx, podTS, experiment.Timeout(), labelSelector,
		)
	}()

	// Log parser in background (waits for pod to appear, then tails)
	logDone := make(chan error, 1)
	go func() {
		logDone <- o.logParser.WaitForPodAndTail(
			trialCtx, labelSelector, "kserve-container", runtimeType, podTS, experiment.Timeout())
	}()

	// Brief pause to let watchers initialise
	time.Sleep(200 * time.Millisecond)

	// ─── TRIGGER ─────────────────────────────────────────────
	endpoint, err := o.inferenceEndpoint(trialCtx, trial)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to resolve endpoint: %w", err)
	}
	payload := buildInferencePayload(experiment.Prompt, experiment.MaxTokens)

	t0 := time.Now()

	resp, err := o.sendInferenceRequest(trialCtx, endpoint, payload)
	if err != nil {
		cancel() // stop collectors
		return nil, fmt.Errorf("inference request with host %s and endpoint %s failed with error: %w", endpoint.Host, endpoint.URL, err)
	}

	t7 := time.Now()

	// Read response body for token data
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	// For now, first token time = response time (no streaming)
	// TODO: implement streaming response parsing for true TTFT
	firstTokenTime := t7

	if resp.StatusCode != http.StatusOK {
		cancel()
		return nil, fmt.Errorf("inference returned HTTP %d, host: %s, url:%s", resp.StatusCode, endpoint.Host, endpoint.URL)
	}

	// ─── COLLECT ─────────────────────────────────────────────
	// Give collectors a grace period to finish capturing remaining events
	collectCtx, collectCancel := context.WithTimeout(ctx, 10*time.Second)
	defer collectCancel()

	select {
	case <-eventDone:
	case <-podInformerDone:
	case <-collectCtx.Done():
		if o.verbose {
			fmt.Println("    Event watcher timed out on grace period")
		}
	}

	// Cancel the main context to stop the log tailer
	cancel()
	select {
	case <-logDone:
	case <-time.After(5 * time.Second):
		if o.verbose {
			fmt.Println("    Log tailer timed out on shutdown")
		}
	}

	// ─── RESOLVE TIMESTAMPS ──────────────────────────────────
	result := o.resolver.Resolve(trial, rep, t0, t7, firstTokenTime, podTS)

	// Diagnose any instrumentation gaps
	if o.verbose {
		issues := instrument.DiagnoseGaps(&result)
		for _, issue := range issues {
			fmt.Printf("    %s\n", issue)
		}
	}

	// ─── TEARDOWN ────────────────────────────────────────────
	if err := o.teardown(ctx, trial); err != nil {
		if o.verbose {
			fmt.Printf("    WARNING: teardown error: %v\n", err)
		}
	}

	return &result, nil
}

// prepare ensures the system is in a known cold state before triggering.
func (o *Orchestrator) prepare(ctx context.Context, trial config.TrialConfig) error {
	// 1. Verify InferenceService is scaled to zero
	if err := o.ensureScaledToZero(ctx, trial); err != nil {
		return fmt.Errorf("ensuring scale-to-zero: %w", err)
	}

	// 2. Drop page cache if scenario requires it
	if trial.Scenario.DropPageCache {
		nodeName, err := o.getGPUNodeName(ctx)
		if err != nil {
			return fmt.Errorf("finding GPU node: %w", err)
		}
		if o.verbose {
			fmt.Printf("    Dropping page cache on node %s...\n", nodeName)
		}
		if err := o.cacheDropper.Drop(ctx, nodeName); err != nil {
			return fmt.Errorf("dropping page cache: %w", err)
		}
	}

	return nil
}

// ensureScaledToZero waits until no pods exist for the InferenceService.
func (o *Orchestrator) ensureScaledToZero(ctx context.Context, trial config.TrialConfig) error {
	selector := inferenceServiceSelector(trial)

	// Poll until pods = 0 or timeout
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(2 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for scale-to-zero")
		case <-ticker.C:
			pods, err := o.k8sClient.CoreV1().Pods(o.namespace).List(ctx, metav1.ListOptions{
				LabelSelector: selector,
			})
			if err != nil {
				return fmt.Errorf("listing pods: %w", err)
			}
			if len(pods.Items) == 0 {
				return nil
			}
			if o.verbose {
				fmt.Printf("    Waiting for scale-to-zero (%d pods remaining)...\n", len(pods.Items))
			}
		}
	}
}

// teardown ensures the system scales back to zero after a trial.
func (o *Orchestrator) teardown(ctx context.Context, trial config.TrialConfig) error {
	// Wait for Knative to scale down naturally, or force it
	selector := inferenceServiceSelector(trial)

	// Give Knative time to initiate scale-down
	// (the idle timeout is usually 30s, but we don't want to wait that long)
	// Instead, we could patch the deployment to 0 replicas
	// For now, just wait for natural scale-down with a timeout

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(3 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			// Force cleanup: delete pods with the selector
			if o.verbose {
				fmt.Println("    Forcing pod cleanup...")
			}
			return o.k8sClient.CoreV1().Pods(o.namespace).DeleteCollection(
				ctx,
				metav1.DeleteOptions{},
				metav1.ListOptions{LabelSelector: selector},
			)
		case <-ticker.C:
			pods, err := o.k8sClient.CoreV1().Pods(o.namespace).List(ctx, metav1.ListOptions{
				LabelSelector: selector,
			})
			if err != nil {
				continue
			}
			if len(pods.Items) == 0 {
				return nil
			}
		}
	}
}

// getGPUNodeName returns the name of a node with GPU resources.
func (o *Orchestrator) getGPUNodeName(ctx context.Context) (string, error) {
	nodes, err := o.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, node := range nodes.Items {
		// Check for nvidia.com/gpu resource
		if qty, ok := node.Status.Capacity["nvidia.com/gpu"]; ok {
			if qty.Value() > 0 {
				return node.Name, nil
			}
		}
	}

	// Fallback: return first node (single-node cluster)
	if len(nodes.Items) > 0 {
		return nodes.Items[0].Name, nil
	}

	return "", fmt.Errorf("no nodes found in cluster")
}

// sendInferenceRequest sends the HTTP POST to the inference endpoint.
func (o *Orchestrator) sendInferenceRequest(
	ctx context.Context,
	endpoint render.Endpoint,
	payload string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL,
		strings.NewReader(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if endpoint.Host != "" {
		req.Host = endpoint.Host
	}

	return o.httpClient.Do(req)
}

func (o *Orchestrator) inferenceEndpoint(ctx context.Context, trial config.TrialConfig) (render.Endpoint, error) {
	isvcName := inferenceServiceName(trial)
	statusURL, err := k8s.InferenceServiceClusterURL(ctx, o.dynamicClient, o.namespace, isvcName)
	if err != nil {
		return render.Endpoint{}, fmt.Errorf("Error: Failed to get inference status url: %w", err)
	}

	return render.InferenceEndpoint(
		*o.endpointConfig,
		isvcName,
		o.namespace,
		statusURL,
	), nil
}

func buildInferencePayload(prompt string, maxTokens int) string {
	return fmt.Sprintf(`{"prompt": %q, "max_tokens": %d}`, prompt, maxTokens)
}

// inferenceServiceName derives the KServe InferenceService name from a trial config.
func inferenceServiceName(trial config.TrialConfig) string {
	return render.ISVCName(
		trial.Runtime.Name,
		trial.Format.Name,
		trial.Caching.Name,
		trial.Model.Name,
	)
}

// inferenceServiceSelector returns the label selector for pods belonging
// to a KServe InferenceService.
func inferenceServiceSelector(trial config.TrialConfig) string {
	isvcName := inferenceServiceName(trial)
	return fmt.Sprintf("serving.kserve.io/inferenceservice=%s", isvcName)
}

// detectRuntimeType determines the RuntimeType from a trial config.
func detectRuntimeType(trial config.TrialConfig) instrument.RuntimeType {
	name := strings.ToLower(trial.Runtime.Name)
	if strings.Contains(name, "vllm") {
		return instrument.RuntimeVLLM
	}
	return instrument.RuntimeLlamaCpp
}

// Close shuts down the orchestrator and flushes output files.
func (o *Orchestrator) Close() error {
	return o.csvWriter.Close()
}
