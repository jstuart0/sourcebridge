# Server-Side QA Rollout Checklist

Operator checklist for enabling the server-side deep-QA orchestrator
(plan `2026-04-22-deep-qa-server-side-orchestrator.md`, Phase 5).
Work the list top-to-bottom. The flag flip is the last step, not
the first.

## Q5.6 — Why the flag stays off by default

`QAConfig.ServerSideEnabled` is `false` in `config.Defaults()` and
intentionally will not be flipped in-tree until the Phase-4 parity
benchmark clears the Decision Rule (see next section) with a
reviewed regression table. Shipping default-on before evidence
would push untested behavior onto every fresh install.

The operator flips the flag per-deployment when they are ready and
satisfied with the parity report. Rollback is a single
`SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED=false` + restart.

## Gate: parity benchmark (plan §Phase 4)

Before flipping `SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED`:

- [ ] `benchmarks/qa/questions.yaml` has ≥ 120 questions across 3
      repos with the class distribution the plan defines. **Already
      authored 2026-04-22 (120 Q across sourcebridge / acme-api /
      multi-lang-repo).**
- [ ] `benchmarks/qa/seed.sh` has been run against the target
      instance so the 3 repos are indexed + understanding corpora
      are ready.
- [ ] Baseline arm run (Python subprocess):
      `benchmarks/qa/cmd/runner -arm=baseline -mode=deep ...`
- [ ] Candidate arm run (against a *non-production* server with the
      flag on):
      `benchmarks/qa/cmd/runner -arm=candidate -mode=deep ...`
- [ ] LLM-as-judge passes over both arms. Uses Claude Opus 4.7 by
      default. On thor, the key lives at
      `automation/anthropic-api-credentials` (field `api-key`):
      ```bash
      export ANTHROPIC_API_KEY=$(kubectl -n automation get secret \
        anthropic-api-credentials -o jsonpath='{.data.api-key}' | base64 -d)
      python3 benchmarks/qa/judge.py \
        --run benchmarks/qa/reports/<date>_baseline/run.jsonl \
        --out benchmarks/qa/reports/<date>_baseline/judgments.yaml
      ```
      Run once per arm. Judgments are cached by `(question, answer)`
      hash so `--resume` re-uses work on retries.
- [ ] Paired report committed:
      `benchmarks/qa/reports/<date>_baseline-vs-candidate/report.md`
- [ ] Decision Rule checks in the report all PASS:
      - overall answer-useful within ±7%
      - per-class within ±10%
      - latency p95 within 2× baseline
- [ ] Top-20 regressions table reviewed and signed off by a human
      (sign-off captured in the Plane / Linear rollout ticket).

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
- [ ] Telemetry: confirm the install's next ping reports
      `features` includes `qa_server_side` and `counts.qa_asks_total_14d`
      ticks up with traffic.

## Observability

- [ ] Monitor dashboard shows `qa` subsystem rows (the filter is
      auto-populated — it renders any subsystem that appears in
      `by_subsystem`, so qa surfaces after the first ask).
- [ ] Latency SLO alert (`p95 < 15s` sync, `degraded-mode success > 99%`)
      is live and firing into on-call. See
      `docs/going-to-production.md` for alert wiring.

## Rollback

If parity regresses or the latency SLO burns:

- [ ] `SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED=false` and restart.
- [ ] The CLI falls back automatically — the `X-SourceBridge-QA`
      capability header disappears from `/healthz`.
- [ ] `sourcebridge ask --legacy` remains available regardless of
      the flag so users can always route around a bad server state.
- [ ] `discussCode` resolver: the flag gate at
      `schema.resolvers.go` returns early when `r.QA == nil`, so the
      legacy direct-to-worker codepath reactivates on restart with
      no additional toggle.

## Q6.1 — Python deep-path deletion (Phase 6)

Phase 6 is **gated on one release cycle of clean production traffic**
after the flag flip above. Do not execute Phase 6 until:

- [ ] At least one release cycle has passed since `ServerSideEnabled=true`
      became the production state on this deployment.
- [ ] Monitor latency p95 stayed within the SLO across the release.
- [ ] No critical regressions (parity or UX) reported via feedback
      channels or support.
- [ ] `counts.qa_asks_total_14d` shows sustained volume on
      the telemetry dashboard, confirming real adoption.

When all four are met, Phase 6 executes the deletions listed in the
plan's §Phase 6: remove `_build_deep_context`, `_load_deep_understanding`,
`_load_summary_evidence`, `_best_deep_files`, `_deep_path_boosts`,
`DeepUnderstandingContext` from `workers/cli_ask.py`; make
`--mode=deep` dispatch error out with a pointer to the server endpoint;
retire the matching tests in `workers/tests/`.

Q6.1 **honors the gate** — we are not deleting the Python deep path
ahead of time, even on a dev branch, because production regressions
on deep-QA have no workaround besides the legacy subprocess.

## Q7.1 — Legacy retirement policy (Phase 7)

Explicitly **deferred** per the plan. The plan says:

> The plan does not commit to A or B; the decision belongs in a
> follow-up plan once we have one release of production data under
> Phase 5.

Options A (retire `cli_ask.py` entirely; requires a local-mode
server-side fast path) vs. B (keep `cli_ask.py` as local-only
fallback forever) will be chosen in a follow-up plan, not here.
Until that decision:

- `sourcebridge ask --legacy --mode fast` works and is not
  deprecated (Ledger F13 — local-desktop working-tree visibility).
- `sourcebridge ask --legacy --mode deep` will error after Phase 6
  completes, with a pointer at the server endpoint.

## Post-rollout

- [ ] Confirm Python `cli_ask.py deep` emits the deprecation warning
      (set `SOURCEBRIDGE_QA_LEGACY=1` in CI jobs that must stay
      quiet).
- [ ] Schedule the Phase-6 Python deletion review at the next
      release-cycle retrospective.
- [ ] File Phase-7 decision ticket capturing Option A vs B with
      production data attached.
