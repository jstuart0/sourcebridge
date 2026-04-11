# 2026-04-11 Report Quality Remediation Status

## Scope

This note tracks the live remediation work for poor enterprise report quality against the deployed `MACU Residence` report path at `https://sourcebridge-enterprise.xmojo.net`.

## Repo / Target

- Enterprise site: `https://sourcebridge-enterprise.xmojo.net`
- Repository under evaluation: `MACU Residence`
- Repository ID: `149fb476-6f73-4e66-ab6b-f18498197c73`

## What Was Fixed

- Wired `analysisDepth` end-to-end so report requests can actually run deep analyzers.
- Persisted report markdown and evidence in the DB instead of relying only on the filesystem.
- Fixed analyzer repo-path handling so deep analysis can use the live repo cache.
- Added real evidence return/persistence over the report path.
- Hardened section generation to fail closed for missing `cicd_detection` evidence instead of inventing deployment topology.
- Removed fake evidence markers, markdown fence leakage, and most repeated validation scaffolding.
- Added live benchmark harness and trend tracking in `benchmarks/results/report-quality-live/`.

## Key Live Runs

- `20260411T175159Z-macu-residence-deep`
  - report `3f1676db-2bbf-4c2e-9551-ef9d869c718e`
  - `quality_score=64`
  - first materially useful deep-analysis run after analyzer path fixes

- `20260411T183055Z-macu-residence-deep`
  - report `a15f6fe3-97d1-440f-9ea9-5dca9bf6935d`
  - `quality_score=91`
  - strongest heuristic score
  - still contained overclaims in `third_party_services` and `package_dependencies`

- `20260411T184022Z-macu-residence-deep`
  - report `7bbfe9e4-7683-4cb3-b015-14fcdf51dabe`
  - `quality_score=81`
  - lower heuristic score than the `91` run
  - better trust boundary in the two worst sections:
    - `third_party_services` reduced to evidence-backed `Supabase`, `Vercel`, `OneLogin`
    - `package_dependencies` stopped inventing deprecation/replacement narratives

## Current Recommendation

Use the trust-hardened worker behavior as the baseline, even though its heuristic score is lower than the peak `91` run.

Reason:

- The `91` run looked cleaner, but it still fabricated materially important details about third-party services and dependency risk.
- The current trust-hardened behavior is closer to something defensible in a client-facing setting.
- Remaining problems are now mostly presentation/noise problems rather than high-severity factual invention.

## Current Live Worker Tag

- `ent-20260411-144002`

## Remaining Issues

- Generic `**Current State.**` repetition is still higher than desired.
- CI/CD-absent sections repeat the same fail-closed sentence several times across the report.
- Some sections still drift into generic filler when only weak metadata exists.
- `Integration and System Interactions` / `Data Inventory and Handling` still overuse cliff-note style fragments instead of concise synthesized conclusions.

## Suggested Next Moves

1. Add section-specific compact fallback templates for:
   - `system_availability`
   - `deployment_architecture`
   - `build_deployment`
   - `monitoring_config`
   - `backup_recovery`
   So the fail-closed output is shorter and less repetitive.

2. Add deterministic renderers for:
   - `third_party_services`
   - `package_dependencies`
   This will remove the last high-risk prompt-only sections.

3. Reduce reuse of raw cliff-note fragments in:
   - `data_inventory`
   - `integrations`
   by summarizing structured evidence instead of replaying file-purpose bullets.

4. Update the benchmark harness to separate:
   - transient worker-unavailable deployment race failures
   - content-quality regressions

## Validation Commands Used

- `go test ./internal/reports ./internal/api ./persistence`
- overlay worker pytest:
  - `PYTHONPATH="$tmpdir:/Users/jaystuart/dev/sourcebridge" .venv/bin/python -m pytest ...`
- live benchmark:
  - `make benchmark-report-quality-live REPORT_BASE_URL=https://sourcebridge-enterprise.xmojo.net REPORT_REPO_NAME='MACU Residence' REPORT_RESULTS_DIR=benchmarks/results/report-quality-live`
