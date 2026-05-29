# 🕷 Anansi

**Find for Cold-Starts on Serverless LLM Inference**

Anansi is a benchmarking harness that systematically measures and decomposes cold-start latency for LLM inference on serverless Kubernetes infrastructure (KServe/Knative).

Named after Anansi the spider of Akan mythology, who tried to attend every feast at once by tying ropes to each pot, only to discover that every path leads to a bottleneck. This tool finds where the ropes pull tightest.

## Features

- **Matrix evaluation** — Cartesian product of runtimes × formats × models × caching × scenarios with automatic exclusion filtering
- **Cold-start decomposition** — Per-component timing (orchestration, runtime init, model loading, initialisation, warm-up) with validation
- **Arrival rate sweep** — M/G/1 queuing model validation against empirical cold-start probability
- **Throughput benchmarking** — Sustained concurrent load measurement (tokens/s, latency percentiles)
- **Statistical analysis** — Median, P95, P99, bootstrap CI, Welch's t-test, Cohen's d
- **Reproducibility** — Deterministic config hashing, raw data export, structured output

## Quick Start

```bash
# Build
make build

# Validate config (no cluster needed)
./bin/anansi validate --config configs/matrix-full.yaml

# Dry run (shows plan without executing)
./bin/anansi run --config configs/matrix-smoke.yaml --dry-run

# Deploy benchmark infrastructure
make deploy

# Run smoke test
./bin/anansi run --config configs/matrix-smoke.yaml --reps 3 --output results/smoke/

# Run full matrix
./bin/anansi run --config configs/matrix-full.yaml --output results/full/

# Generate report
./bin/anansi report --input results/full/
```

## Commands

| Command             | Description                        |
| ------------------- | ---------------------------------- |
| `anansi run`        | Full matrix cold-start evaluation  |
| `anansi decompose`  | Per-component cold-start analysis  |
| `anansi sweep`      | Arrival rate sweep (queuing model) |
| `anansi throughput` | Warm-state throughput benchmark    |
| `anansi report`     | Statistical analysis and report    |
| `anansi validate`   | Config validation (no cluster)     |

## Configuration

See [`configs/`](configs/) for example configurations. The YAML schema supports:

- Multiple runtimes (llama.cpp default/pipelined, vLLM default/faststart)
- Multiple checkpoint formats (SafeTensors FP16, GGUF Q8, GGUF Q4_K_M)
- Multiple models (Phi-3-mini 3.8B, Llama-3-8B, Llama-2-13B)
- Caching strategies (remote MinIO, LocalModelCache)
- Cold-start scenarios (remote/cold, LMC/cold page cache, LMC/warm page cache)

## Requirements

- Go 1.22+
- Kubernetes cluster with GPU (for execution)
- KServe + Knative Serving installed
- `kubectl` configured with cluster access

## License

MIT
