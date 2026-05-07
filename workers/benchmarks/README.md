# workers/benchmarks

Scripts that benchmark or reproduce behavior of the Python worker's comprehension pipeline.
These scripts import worker internals directly (no HTTP); they are distinct from `scripts/`,
which contains end-to-end API-level tools that talk to a running server over HTTP.

## Benchmark scripts

| Script | Purpose |
|---|---|
| `run_comprehension_bench.py` | OSS-safe comprehension benchmarks against fixture and fake-provider cases |
| `run_deep_depth_bench.py` | DEEP cliff notes benchmark across selected OpenRouter models |
| `run_local_sweep.py` | DEEP-from-understanding sweep across local Ollama models |
| `run_medium_sweep.py` | Medium-depth sweep across local models |
| `run_other_artifacts_bench.py` | Benchmark against non-cliff-notes artifact types |
| `run_other_artifacts_parallel.py` | Parallel variant of the other-artifacts bench |
| `run_other_artifacts_top5.py` | Top-5 model comparison for other artifact types |
| `run_cloud_addenda.py` | Cloud-model addenda to local sweep results |
| `analyze_local_sweep.py` | Post-sweep qualitative analyzer for local model results |

## Repro scripts

| Script | Artifact | Purpose |
|---|---|---|
| `repro_qwen_confidence_regression.py` | `benchmark-results/qwen3.6-rerun-ca169/qwen3.6-35b-a3b-moe/artifacts/qwen3.6-35b-a3b-moe-deep_from_understanding.json` | Confirms CA-173 Phases 1-3 recover NDJSON-formatted DEEP sections that qwen3.6 emitted instead of a JSON array (confidence regression) |

### Running the qwen3.6 repro

```bash
cd /path/to/sourcebridge
uv run --project workers python workers/benchmarks/repro_qwen_confidence_regression.py \
  --artifact benchmark-results/qwen3.6-rerun-ca169/qwen3.6-35b-a3b-moe/artifacts/qwen3.6-35b-a3b-moe-deep_from_understanding.json
```
