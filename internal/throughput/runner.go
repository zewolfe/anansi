// Package throughput implements sustained-load throughput benchmarking
// for warm-state LLM inference measurement.
package throughput

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zewolfe/anansi/internal/config"
)

type Runner struct {
	httpClient *http.Client
	host       string
	verbose    bool
}

func NewRunner(host string, verbose bool) *Runner {
	return &Runner{
		httpClient: &http.Client{Timeout: 2 * time.Minute},
		host:       host,
		verbose:    verbose,
	}
}

type RequestResult struct {
	StartTime    time.Time
	EndTime      time.Time
	LatencyMs    float64
	TokenCount   int
	StatusCode   int
	Error        string
	ResponseBody string
}

// RunLevel executes a throughput benchmark at a single concurrency level.
// It sends concurrent requests for the given duration and collects per-request metrics.
func (r *Runner) RunLevel(
	ctx context.Context,
	url string,
	prompt string,
	maxTokens int,
	concurrency int,
	duration time.Duration,
) ([]RequestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, duration+30*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		results  []RequestResult
		wg       sync.WaitGroup
		stopAt   = time.Now().Add(duration)
		reqCount atomic.Int64
	)

	for w := range concurrency {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			payload := fmt.Sprintf(`{"prompt": %q, "max_tokens": %d}`, prompt, maxTokens)

			for time.Now().Before(stopAt) {
				select {
				case <-ctx.Done():
					return
				default:
				}

				result := r.sendRequest(ctx, url, payload)
				reqCount.Add(1)

				mu.Lock()
				results = append(results, result)
				mu.Unlock()

				if r.verbose && reqCount.Load()%10 == 0 {
					fmt.Printf("    [worker %d] %d requests completed\n",
						workerID, reqCount.Load())
				}
			}
		}(w)
	}

	wg.Wait()
	return results, nil
}

// sendRequest sends a single inference request and captures timing.
func (r *Runner) sendRequest(ctx context.Context, url, payload string) RequestResult {
	result := RequestResult{
		StartTime: time.Now(),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(payload))
	if err != nil {
		result.Error = err.Error()
		result.EndTime = time.Now()
		result.LatencyMs = float64(result.EndTime.Sub(result.StartTime).Milliseconds())
		return result
	}
	req.Header.Set("Content-Type", "application/json")

	if r.host != "" {
		req.Host = r.host
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		result.Error = err.Error()
		result.EndTime = time.Now()
		result.LatencyMs = float64(result.EndTime.Sub(result.StartTime).Milliseconds())
		return result
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	result.EndTime = time.Now()
	result.LatencyMs = float64(result.EndTime.Sub(result.StartTime).Milliseconds())
	result.StatusCode = resp.StatusCode
	result.ResponseBody = string(body)

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	// Estimate token count from response length
	// (rough heuristic: ~4 chars per token for English)
	result.TokenCount = estimateTokens(result.ResponseBody)

	return result
}

// Warmup sends a few requests to ensure the model is loaded and ready.
func (r *Runner) Warmup(ctx context.Context, url string, prompt string, maxTokens int, count int) error {
	payload := fmt.Sprintf(`{"prompt": %q, "max_tokens": %d}`, prompt, maxTokens)

	for i := range count {
		if r.verbose {
			fmt.Printf("    Warmup request %d/%d...\n", i+1, count)
		}
		result := r.sendRequest(ctx, url, payload)
		if result.Error != "" {
			return fmt.Errorf("warmup request %d failed: %s", i+1, result.Error)
		}
	}
	return nil
}

// Aggregate computes throughput metrics from a set of request results.
func Aggregate(
	results []RequestResult,
	concurrency int,
	cfgHash string,
	runtime string,
	format string,
	model string,
) config.ThroughputResult {
	tr := config.ThroughputResult{
		ConfigHash:  cfgHash,
		Runtime:     runtime,
		Format:      format,
		Model:       model,
		Concurrency: concurrency,
	}

	if len(results) == 0 {
		return tr
	}

	var latencies []float64
	totalTokens := 0
	errors := 0
	minStart := results[0].StartTime
	maxEnd := results[0].EndTime

	for _, r := range results {
		tr.TotalRequests++
		if r.Error != "" {
			errors++
			continue
		}

		latencies = append(latencies, r.LatencyMs)
		totalTokens += r.TokenCount

		if r.StartTime.Before(minStart) {
			minStart = r.StartTime
		}
		if r.EndTime.After(maxEnd) {
			maxEnd = r.EndTime
		}
	}

	wallTime := maxEnd.Sub(minStart).Seconds()
	tr.DurationSec = wallTime
	tr.TotalTokens = totalTokens
	tr.ErrorRate = float64(errors) / float64(tr.TotalRequests)

	if wallTime > 0 {
		tr.TokensPerSec = float64(totalTokens) / wallTime
	}

	if len(latencies) > 0 {
		sort.Float64s(latencies)
		tr.MedianLatMs = percentile(latencies, 50)
		tr.P95LatMs = percentile(latencies, 95)
		tr.P99LatMs = percentile(latencies, 99)
	}

	return tr
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
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

func estimateTokens(response string) int {
	// Rough estimate: ~4 characters per token for English text
	if len(response) < 4 {
		return 1
	}
	return len(response) / 4
}
