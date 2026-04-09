# Comprehension Engine Execution Status

Date: 2026-04-09
Plan: `thoughts/shared/plans/2026-04-09-comprehension-engine-and-llm-orchestration.md`
Purpose: execution log and handoff state for autonomous implementation

## Current focus

- Active phase: `A0` and `A1`
- Objective:
  - add baseline benchmark harness
  - add worker-side reliability guardrails that are locally testable
  - keep committed artifacts OSS-safe

## Completed in this pass

- Revised the architecture plan to:
  - make hierarchical the production contract
  - make advanced strategies progressive enhancements
  - add evidence-gated phases
  - add benchmark/tag/push workflow
  - add OSS-release discipline
  - add concrete A0 benchmark harness plan
- Implemented A0 benchmark scaffolding:
  - `benchmarks/comprehension/manifest.yaml`
  - `workers/benchmarks/run_comprehension_bench.py`
  - `workers/benchmarks/__init__.py`
  - `Makefile` targets for fake benchmark runs and report viewing
- Implemented worker-side explicit empty-response handling:
  - `workers/common/llm/provider.py`
  - wired into knowledge/reasoning direct LLM callsites
- Hardened gRPC metadata lookup:
  - `workers/common/grpc_metadata.py`
- Implemented API/store-side Phase A persistence work:
  - `internal/knowledge/models.go`
  - `internal/knowledge/store.go`
  - `internal/knowledge/memstore.go`
  - `internal/db/knowledge_store.go`
  - `internal/db/migrations/014_knowledge_artifact_errors.surql`
- Implemented resolver-side failure classification and persistence:
  - `internal/api/graphql/schema.resolvers.go`
  - structured codes currently classified as:
    - `DEADLINE_EXCEEDED`
    - `WORKER_UNAVAILABLE`
    - `LLM_EMPTY`
    - `SNAPSHOT_TOO_LARGE`
    - `INTERNAL`
- Added minimal refresh dedupe:
  - `refreshKnowledgeArtifact` now returns the existing artifact if already `GENERATING` / `PENDING`
- Exposed persisted artifact failure metadata through GraphQL/UI:
  - `internal/api/graphql/schema.graphqls`
  - `internal/api/graphql/models_gen.go`
  - `internal/api/graphql/helpers.go`
  - `web/src/lib/graphql/queries.ts`
  - `web/src/app/(app)/repositories/[id]/page.tsx`
- Added tests:
  - `workers/tests/test_llm_provider.py`
  - `workers/tests/test_comprehension_bench.py`

## In progress in this pass

- Full orchestrator/monitor work is still pending.
- Broader mutation dedupe beyond `refreshKnowledgeArtifact` is still pending.

## Added in latest slice

- Implemented a real-provider benchmark path for the existing fixture suite:
  - `workers/benchmarks/run_comprehension_bench.py` now supports `--provider-mode live`
  - `make benchmark-comprehension-local` now runs the same fixture cases against the configured worker LLM provider
- Kept live-provider benchmark outputs OSS-safer by design:
  - no prompt/output text is written to benchmark artifacts
  - live-provider failures are sanitized to exception type + generic failure marker
- Added worker test coverage for:
  - provider-mode override behavior
  - live-provider error sanitization
  - live-provider execution path with a patched provider factory
- Prior slice completed:
  - extended persisted failure detail surfacing to workflow story, learning path, and code tour
  - added direct memstore coverage for failure metadata behavior

## Next recommended steps if handed off

1. Finish A0 code scaffolding:
   - decide whether committed fixture benchmark results should stay in-tree or only regenerated in CI/local workflows
   - run `benchmark-comprehension-local` in the intended thor/Ollama environment and archive the sanitized report
2. Finish A1/A2 API-side work:
   - broaden dedupe beyond refresh where still needed
   - decide whether any admin/ops views should surface persisted artifact failure metadata
3. Add a phase report artifact under an OSS-safe path.
4. Only then consider broader orchestrator/monitor work.

## Constraints / notes

- `thoughts/` is currently untracked in git.
- Keep benchmark artifacts OSS-safe.
- Do not commit private repo outputs or thor-specific raw logs.

## Commands run

Successful:

- `cd workers && uv sync`
- `cd workers && uv run python -m pytest tests/test_llm_provider.py tests/test_comprehension_bench.py tests/test_cliff_notes.py tests/test_learning_path.py tests/test_code_tour.py tests/test_workflow_story.py -v`
- `make benchmark-comprehension-fake BENCHMARK_RESULTS_DIR=benchmarks/results/local-checkpoint`
- `make benchmark-comprehension-report BENCHMARK_RESULTS_DIR=benchmarks/results/local-checkpoint`
- `cd workers && uv run python -m pytest tests/test_requirements_servicer.py -v`
- `cd workers && uv run python -m pytest tests/test_comprehension_bench.py -v`
- `go test ./internal/api/graphql ./internal/knowledge ./internal/db`
- `go test ./internal/api/graphql`
- `go test ./internal/knowledge ./internal/api/graphql`
- `make benchmark-comprehension-fake BENCHMARK_RESULTS_DIR=benchmarks/results/local-checkpoint`
- `make benchmark-comprehension-report BENCHMARK_RESULTS_DIR=benchmarks/results/local-checkpoint`

Frontend validation resolved in this environment:

- `cd web && npm ci`
- `cd web && npm run lint -- --file 'src/app/(app)/repositories/[id]/page.tsx' --file src/lib/graphql/queries.ts`
  - passed with no ESLint warnings or errors
- `cd web && npm run lint -- --file 'src/app/(app)/repositories/[id]/page.tsx'`
  - passed with no ESLint warnings or errors

Observed issue fixed during this pass:

- `cd workers && uv run python -m pytest tests/test_reviewer.py tests/test_discussion.py tests/test_summarizer.py tests/test_explainer.py tests/test_requirements_servicer.py -v`
  - initially failed because `workers/common/grpc_metadata.py` assumed all mock contexts implemented `invocation_metadata()`
  - fixed by making `resolve_model_override()` tolerate contexts without that method

## OSS-safe outputs produced

- `benchmarks/results/local-checkpoint/summary.json`
- `benchmarks/results/local-checkpoint/report.md`
- per-case JSON results under `benchmarks/results/local-checkpoint/`

These outputs are fixture/fake-provider based and safe to commit.
