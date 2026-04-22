# Server-Side QA Rollout Checklist

Operator checklist for enabling the server-side deep-QA orchestrator
(plan `2026-04-22-deep-qa-server-side-orchestrator.md`, Phase 5).
Work the list top-to-bottom. The flag flip is the last step, not
the first.

## Gate: parity benchmark (plan §Phase 4)

Before flipping `SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED`:

- [ ] `benchmarks/qa/questions.yaml` has ≥ 120 questions across 3
      repos with the class distribution the plan defines.
- [ ] `benchmarks/qa/labels.yaml` has an adjudicated 0..3 label for
      every question (two reviewers, disputes resolved before
      freeze).
- [ ] Baseline arm run: `benchmarks/qa/cmd/runner -arm=baseline ...`
- [ ] Candidate arm run (against a *non-production* server with the
      flag on): `benchmarks/qa/cmd/runner -arm=candidate ...`
- [ ] Paired report committed:
      `benchmarks/qa/reports/<date>_baseline-vs-candidate/report.md`
- [ ] Decision Rule checks in the report all PASS:
      - overall answer-useful within ±7%
      - per-class within ±10%
      - latency p95 within 2× baseline
- [ ] Top-20 regressions table reviewed and signed off by a human.

If any gate is unmet, do not flip the flag. Ship parity first.

## Deployment flip

Per-deployment, on the operator's schedule:

- [ ] Set `SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED=true` in the target
      environment (k8s secret / systemd unit / docker-compose env).
- [ ] Restart / re-roll the Go server so the flag is picked up by
      `handleHealthz`, the REST handler, and the MCP tool dispatch.
- [ ] `curl -I https://<server>/healthz | grep X-SourceBridge-QA`
      should return `X-SourceBridge-QA: v1`.
- [ ] `curl -X POST https://<server>/api/v1/ask` with a trivial
      payload returns a structured AskResult (not 503).
- [ ] `sourcebridge ask --repository-id <id> "small question"`
      routes to the server (add `--json` to see the structured
      response).
- [ ] MCP tool listing via a client (Claude Code, Codex) shows
      `ask_question` alongside `explain_code`.

## Observability

- [ ] Monitor dashboard filter includes the `qa` subsystem (added by
      plan §Ramification 7; confirm the panel renders qa.* stages).
- [ ] Latency SLO alert (`p95 < 15s`) is live and firing into
      on-call.

## Rollback

If parity regresses or latency SLO burns:

- [ ] `SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED=false` and restart.
- [ ] The CLI falls back automatically — the capability header
      disappears from `/healthz`.
- [ ] `sourcebridge ask --legacy` remains available regardless of
      the flag; users can always route around a bad server state.

## Post-rollout

- [ ] Confirm Python `cli_ask.py deep` emits the deprecation warning
      (set `SOURCEBRIDGE_QA_LEGACY=1` in CI jobs that must stay
      quiet).
- [ ] Schedule the Phase-6 Python deletion after one release cycle
      of clean production traffic.
