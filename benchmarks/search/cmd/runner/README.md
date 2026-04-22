# Hybrid Retrieval Benchmark Runner

Invokes the running SourceBridge GraphQL API once per frozen benchmark
query and writes a JSONL artifact for downstream analysis.

This tool is intentionally dumb — it **does not** compute MRR, NDCG,
or latency percentiles. That is the report script's job, kept
separate so baseline and candidate runs can be analyzed with the same
code regardless of which implementation produced the records.

## Usage

```bash
# 1. Start SourceBridge locally (API at http://localhost:8080)
make dev

# 2. Run the frozen query set against the current build
go run ./benchmarks/search/cmd/runner \
  -url http://localhost:8080/api/v1/graphql \
  -token "$(cat ~/.sourcebridge/token)" \
  -label baseline

# 3. Switch to the candidate branch / flip the feature flag, repeat
go run ./benchmarks/search/cmd/runner \
  -url http://localhost:8080/api/v1/graphql \
  -token "$(cat ~/.sourcebridge/token)" \
  -label candidate \
  -out benchmarks/search/reports/2026-04-22_baseline_vs_hybrid/
```

Both invocations write `.jsonl` files into the same output directory
when `-out` is shared. The runner also drops `environment.yaml` so
the environment manifest required by the plan's evaluation protocol
is part of every report.

## Required workflow (from the plan)

1. Freeze `queries.yaml` and `labels.yaml` before the comparison run.
2. Run `-label baseline` against the legacy substring implementation
   (e.g. with the hybrid feature flag off, or on the pre-hybrid
   commit).
3. Run `-label candidate` against the new hybrid service.
4. Use the same SurrealDB snapshot, model IDs, and cache policy for
   both runs — record them in `environment.yaml` (the runner seeds
   this file; fill in the extra fields by hand).
5. Hand both `.jsonl` files to the report script / notebook to
   compute deltas.

See
[thoughts/shared/plans/2026-04-22-hybrid-retrieval-search.md](../../../thoughts/shared/plans/2026-04-22-hybrid-retrieval-search.md)
§Evaluation Protocol for the full rules.
