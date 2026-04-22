# QA Parity Benchmark

Deep-QA parity harness for the server-side orchestrator migration
(plan `2026-04-22-deep-qa-server-side-orchestrator.md`, Phase 4).

## What this proves

Paired before/after comparison of answer-useful rate, latency, and
fallback rate across a frozen 120-question set. The candidate
(server-side QA) ships only when the paired report clears the
§Decision Rule defined in the plan:

- overall answer-useful rate within ±7%
- no question class down > 10% answer-useful rate
- latency p95 within 2× the Python baseline
- top-20 regressions reviewed and signed off by a human

## Layout

- `questions.yaml` — frozen question set (120 questions, append-only)
- `seed.sh`        — clones + imports the 3 benchmark repos into a
                     running SourceBridge instance
- `cmd/runner/`    — Go binary that drives one arm and writes
                     `run.jsonl` + `environment.yaml`
- `judge.py`       — LLM-as-judge (Claude Opus 4.7 by default) that
                     scores each answer 0-3 against its question and
                     writes `judgments.yaml`
- `report.py`      — paired analyzer that compares two arms and
                     emits `report.md` with the Decision Rule checks
- `reports/`       — per-date directories holding paired reports

## End-to-end pipeline

```bash
# 1. Start a SourceBridge server with QA enabled:
SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED=true make dev   # in one terminal

# 2. Seed the three benchmark repos:
SOURCEBRIDGE_URL=http://localhost:8080 \
SOURCEBRIDGE_API_TOKEN="$(cat ~/.sourcebridge/token)" \
benchmarks/qa/seed.sh > /tmp/seed-output.jsonl

# 3. Baseline arm (Python subprocess):
(cd benchmarks/qa/cmd/runner && go build -o ../../runner .)
benchmarks/qa/runner -arm=baseline \
    -questions=benchmarks/qa/questions.yaml \
    -mode=deep \
    -workers-dir=workers \
    -out=benchmarks/qa/reports/$(date +%Y-%m-%d)_baseline

# 4. Candidate arm (server /api/v1/ask):
benchmarks/qa/runner -arm=candidate \
    -questions=benchmarks/qa/questions.yaml \
    -mode=deep \
    -server-url=http://localhost:8080 \
    -repository-id=<repo-id-from-seed.sh> \
    -api-token="$SOURCEBRIDGE_API_TOKEN" \
    -out=benchmarks/qa/reports/$(date +%Y-%m-%d)_candidate

# 5. Judge both arms (LLM-as-judge).
#    On thor, pull the key from the canonical automation secret:
export ANTHROPIC_API_KEY=$(kubectl -n automation get secret \
    anthropic-api-credentials -o jsonpath='{.data.api-key}' | base64 -d)
#    (off-cluster: export ANTHROPIC_API_KEY=sk-ant-... from your own vault)

python3 benchmarks/qa/judge.py \
    --run benchmarks/qa/reports/$(date +%Y-%m-%d)_baseline/run.jsonl \
    --out benchmarks/qa/reports/$(date +%Y-%m-%d)_baseline/judgments.yaml

python3 benchmarks/qa/judge.py \
    --run benchmarks/qa/reports/$(date +%Y-%m-%d)_candidate/run.jsonl \
    --out benchmarks/qa/reports/$(date +%Y-%m-%d)_candidate/judgments.yaml

# 6. Paired report:
python3 benchmarks/qa/report.py \
    --baseline  benchmarks/qa/reports/$(date +%Y-%m-%d)_baseline \
    --candidate benchmarks/qa/reports/$(date +%Y-%m-%d)_candidate \
    --out       benchmarks/qa/reports/$(date +%Y-%m-%d)_baseline-vs-candidate/report.md
```

## LLM-as-judge protocol

The judge sees only `(question, answer, references)`; it never sees
which arm produced the answer. This asymmetric-blind protocol keeps
the paired comparison honest — the judge cannot accidentally favor
one pipeline.

Rubric (0..3):
- 0 — misleading / wrong
- 1 — not useful (doesn't answer the question)
- 2 — useful (answers with minor gaps)
- 3 — excellent (thorough, cites concrete evidence)

Judgments are hashed by `(question, answer)` so re-running the judge
with `--resume` only re-scores rows whose answers have changed.

## Question classes

| Class            | Count | Focus |
|------------------|------:|-------|
| architecture     | 25    | subsystem boundaries, data flow, why-is-it-this-shape |
| ownership        | 25    | where a thing lives (file / function / module) |
| execution_flow   | 25    | step-by-step trace of a request / job |
| cross_cutting    | 25    | security, rate limiting, observability, backwards-compat |
| behavior         | 20    | small functional questions about specific behaviors |

Per-repo distribution:

| Repo            | Q count |
|-----------------|--------:|
| sourcebridge    | 67 |
| acme-api        | 43 |
| multi-lang-repo | 10 |

## Freeze policy

`questions.yaml` is append-only once a baseline report has been
published. Renaming or removing a question silently invalidates every
paired comparison that referenced it. Add new questions at the end;
never edit existing rows in place.
