package instrument

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RuntimeType identifies the serving runtime for log pattern matching.
type RuntimeType string

const (
	RuntimeLlamaCpp RuntimeType = "llamacpp"
	RuntimeVLLM     RuntimeType = "vllm"
)

// LogPattern defines a compiled regex and the runtime it belongs to.
type LogPattern struct {
	Runtime RuntimeType
	Name    string
	Pattern *regexp.Regexp
}

// predefined log patterns for each runtime.
var logPatterns = map[RuntimeType][]LogPattern{
	RuntimeLlamaCpp: {
		{
			Runtime: RuntimeLlamaCpp,
			Name:    "model_loaded",
			// llama.cpp prints: "llm_load_tensors: loaded X tensors ..." or
			// "model loaded in X ms" depending on version
			Pattern: regexp.MustCompile(`(?i)(llm_load_tensors:\s*loaded|model\s+loaded)`),
		},
		{
			Runtime: RuntimeLlamaCpp,
			Name:    "pipelined_loader_done",
			Pattern: regexp.MustCompile(`anansi:\s*pipelined\s+load\s+complete`),
		},
		{
			Runtime: RuntimeLlamaCpp,
			Name:    "server_ready",
			// llama-server prints: "all slots are idle" or server is listening on when ready to serve
			Pattern: regexp.MustCompile(`(?i)(server\s+is\s+listening|all\s+slots\s+are\s+idle)`),
		},
	},
	RuntimeVLLM: {
		{
			Runtime: RuntimeVLLM,
			Name:    "model_loaded",
			// vLLM prints: "Loading model weights took X GB" when weight loading completes
			Pattern: regexp.MustCompile(`Loading\s+model\s+weights\s+took`),
		},
		{
			Runtime: RuntimeVLLM,
			Name:    "cuda_graphs_done",
			// vLLM prints after CUDA graph capture completes
			Pattern: regexp.MustCompile(`(?i)(cuda\s+graph|graph\s+capture.*done|capturing\s+the\s+model)`),
		},
		{
			Runtime: RuntimeVLLM,
			Name:    "server_ready",
			// vLLM prints: "Uvicorn running on" when the HTTP server is ready
			Pattern: regexp.MustCompile(`Uvicorn\s+running\s+on`),
		},
	},
}

// LogMatch represents a single matched log line with its timestamp.
type LogMatch struct {
	Runtime   RuntimeType
	Name      string
	Timestamp time.Time
	Line      string
}

// LogParser tails container logs and matches patterns to extract timestamps.
type LogParser struct {
	client    kubernetes.Interface
	namespace string
}

// NewLogParser creates a parser bound to a namespace.
func NewLogParser(client kubernetes.Interface, namespace string) *LogParser {
	return &LogParser{
		client:    client,
		namespace: namespace,
	}
}

// TailAndParse streams logs from the specified pod/container, matching against
// runtime-specific patterns. Matched lines are sent to the returned channel.
// The function blocks until context cancellation or the stream ends.
func (lp *LogParser) TailAndParse(
	ctx context.Context,
	podName string,
	containerName string,
	runtime RuntimeType,
	matchCh chan<- LogMatch,
) error {
	defer close(matchCh)

	patterns, ok := logPatterns[runtime]
	if !ok {
		return fmt.Errorf("unknown runtime type: %s", runtime)
	}

	stream, err := lp.openLogStream(ctx, podName, containerName)
	if err != nil {
		return fmt.Errorf("opening log stream: %w", err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	// Increase buffer for long log lines (vLLM can be verbose)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	firstLine := true
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line := scanner.Text()
		ts := parseK8sLogTimestamp(line)

		if firstLine {
			firstLine = false
			select {
			case matchCh <- LogMatch{Runtime: runtime, Name: "container_log_start", Timestamp: ts, Line: line}:
			case <-ctx.Done():
				return nil
			}
		}

		for _, pat := range patterns {
			if pat.Pattern.MatchString(line) {
				match := LogMatch{
					Runtime:   runtime,
					Name:      pat.Name,
					Timestamp: ts,
					Line:      line,
				}
				select {
				case matchCh <- match:
				case <-ctx.Done():
					return nil
				}
				break // only first matching pattern per line
			}
		}
	}

	return scanner.Err()
}

// WaitForPodAndTail waits for a pod matching the label selector to appear,
// then tails its logs. This is the main entry point for log-based instrumentation
// when the pod name isn't known in advance
func (lp *LogParser) WaitForPodAndTail(
	ctx context.Context,
	labelSelector string,
	containerName string,
	runtime RuntimeType,
	timestamps *PodTimestamps,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	podName, err := lp.waitForPod(ctx, labelSelector)
	if err != nil {
		return fmt.Errorf("waiting for pod: %w", err)
	}

	matchCh := make(chan LogMatch, 32)

	// Start log tailing in background
	var tailErr error
	go func() {
		tailErr = lp.TailAndParse(ctx, podName, containerName, runtime, matchCh)
	}()

	// Process matches
	for match := range matchCh {
		timestamps.mu.Lock()
		switch match.Name {
		case "container_log_start":
			if timestamps.ContainerLogStart.IsZero() {
				timestamps.ContainerLogStart = match.Timestamp
			}
		case "model_loaded", "pipelined_loader_done":
			if timestamps.ModelLoaded.IsZero() {
				timestamps.ModelLoaded = match.Timestamp
			}
		case "server_ready":
			if timestamps.ServerReady.IsZero() {
				timestamps.ServerReady = match.Timestamp
			}
		}
		ready := !timestamps.ServerReady.IsZero()
		timestamps.mu.Unlock()

		if ready {
			cancel()
		}
	}

	return tailErr
}

// openLogStream opens a follow-mode log stream for a pod.
// Retries until the pod exists and logs are available, or context expires.
func (lp *LogParser) openLogStream(
	ctx context.Context,
	podName string,
	containerName string,
) (io.ReadCloser, error) {
	opts := &corev1.PodLogOptions{
		Follow:     true,
		Timestamps: true, // K8s prepends RFC3339Nano timestamps
	}
	if containerName != "" {
		opts.Container = containerName
	}

	// Retry loop: pod may not exist yet during cold-start
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			req := lp.client.CoreV1().Pods(lp.namespace).GetLogs(podName, opts)
			stream, err := req.Stream(ctx)
			if err == nil {
				return stream, nil
			}
		}
	}
}

// waitForPod watches for a pod matching the selector to appear and returns its name.
func (lp *LogParser) waitForPod(ctx context.Context, labelSelector string) (string, error) {
	watcher, err := lp.client.CoreV1().Pods(lp.namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return "", err
	}
	defer watcher.Stop()

	pods, err := lp.client.CoreV1().Pods(lp.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err == nil && len(pods.Items) > 0 {
		return pods.Items[0].Name, nil
	}

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timeout waiting for pod with selector %s", labelSelector)
		case evt, ok := <-watcher.ResultChan():
			if !ok {
				return "", fmt.Errorf("pod watch channel closed")
			}
			if evt.Type == "ADDED" || evt.Type == "MODIFIED" {
				pod, ok := evt.Object.(*corev1.Pod)
				if ok {
					return pod.Name, nil
				}
			}
		}
	}
}

// parseK8sLogTimestamp extracts the timestamp that Kubernetes
// prepends to log lines when Timestamps: true is set.
// Format: "2026-04-20T14:30:05.123456789Z <rest of line>"
func parseK8sLogTimestamp(line string) time.Time {
	// K8s timestamp is the first space-delimited token
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:idx])
	if err != nil {
		return time.Time{}
	}
	return ts
}
