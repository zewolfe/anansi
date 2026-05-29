package instrument

import (
	"testing"
	"time"

	"github.com/zewolfe/anansi/internal/config"
)

func TestParseK8sLogTimestamp(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{
			"2026-04-20T14:30:05.123456789Z model loaded in 4523 ms",
			"2026-04-20T14:30:05.123456789Z",
		},
		{
			"2026-04-20T14:30:05Z some log line",
			"2026-04-20T14:30:05Z",
		},
		{
			"not a timestamp line",
			"",
		},
		{
			"",
			"",
		},
	}

	for _, tt := range tests {
		got := parseK8sLogTimestamp(tt.line)
		if tt.want == "" {
			if !got.IsZero() {
				t.Errorf("parseK8sLogTimestamp(%q) = %v, want zero", tt.line, got)
			}
			continue
		}
		expected, _ := time.Parse(time.RFC3339Nano, tt.want)
		if !got.Equal(expected) {
			t.Errorf("parseK8sLogTimestamp(%q) = %v, want %v", tt.line, got, expected)
		}
	}
}

func TestExtractNodeFromScheduledMessage(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{
			"Successfully assigned kserve-bench/my-pod-abc123 to gpu-node-1",
			"gpu-node-1",
		},
		{
			"Successfully assigned default/pod to worker-node-2",
			"worker-node-2",
		},
		{
			"some other message",
			"",
		},
		{
			"",
			"",
		},
	}

	for _, tt := range tests {
		got := extractNodeFromScheduledMessage(tt.msg)
		if got != tt.want {
			t.Errorf("extractNodeFromScheduledMessage(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestLogPatternMatching(t *testing.T) {
	tests := []struct {
		runtime RuntimeType
		line    string
		want    string
	}{
		// llama.cpp patterns
		{RuntimeLlamaCpp, "llm_load_tensors: loaded 291 tensors", "model_loaded"},
		{RuntimeLlamaCpp, "model loaded in 4523 ms", "model_loaded"},
		{RuntimeLlamaCpp, "anansi: pipelined load complete in 2341 ms", "pipelined_loader_done"},
		{RuntimeLlamaCpp, "all slots are idle", "server_ready"},
		{RuntimeLlamaCpp, "some random log line", ""},

		// vLLM patterns
		{RuntimeVLLM, "Loading model weights took 7.28 GB", "model_loaded"},
		{RuntimeVLLM, "CUDA graph captured successfully", "cuda_graphs_done"},
		{RuntimeVLLM, "Uvicorn running on http://0.0.0.0:8000", "server_ready"},
		{RuntimeVLLM, "INFO: some other log", ""},
	}

	for _, tt := range tests {
		patterns := logPatterns[tt.runtime]
		matched := ""
		for _, pat := range patterns {
			if pat.Pattern.MatchString(tt.line) {
				matched = pat.Name
				break
			}
		}
		if matched != tt.want {
			t.Errorf("[%s] %q matched %q, want %q",
				tt.runtime, tt.line, matched, tt.want)
		}
	}
}

func TestPodTimestampsHasEssential(t *testing.T) {
	ts := &PodTimestamps{}

	if ts.hasEssentialTimestamps() {
		t.Fatal("empty timestamps should not have essential")
	}

	now := time.Now()
	ts.Scheduled = now
	if ts.hasEssentialTimestamps() {
		t.Fatal("Scheduled alone should not be essential")
	}

	ts.ContainerStart = now.Add(1 * time.Second)
	if ts.hasEssentialTimestamps() {
		t.Fatal("Scheduled + ContainerStart should not be essential")
	}

	ts.ContainersReady = now.Add(2 * time.Second)
	if !ts.hasEssentialTimestamps() {
		t.Fatal("Scheduled + ContainerStart + ContainersReady should be essential")
	}
}

func TestSummaryLine(t *testing.T) {
	result := &config.TrialResult{
		Rep:         3,
		TTFT_ms:     8500,
		TOrch_ms:    2000,
		TRuntime_ms: 500,
		TLoad_ms:    4000,
		TInit_ms:    1500,
	}
	line := SummaryLine(result)
	if line == "" {
		t.Fatal("SummaryLine returned empty for valid result")
	}

	// Error case
	errResult := &config.TrialResult{
		Rep:   1,
		Error: "timeout",
	}
	errLine := SummaryLine(errResult)
	if errLine == "" {
		t.Fatal("SummaryLine returned empty for error result")
	}
}
