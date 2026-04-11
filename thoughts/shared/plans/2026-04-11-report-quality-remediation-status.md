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
- Curated the default `Architecture Baseline` section set for `C-Suite / Board` so the report no longer tries to force the full long-form section catalog into a one-repository baseline.
- Replaced more of the opening and governance-access material with deterministic descriptive rendering rather than open-ended synthesis.
- Cleaned the cover page and appendix output so the final document reads more like a baseline deliverable and less like an exported system artifact.

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

- `20260411T203719Z-macu-residence-deep`
  - report `c1853125-f16a-45e1-9f66-ff26686c81d9`
  - `quality_score=78`
  - used deterministic renderers for `third_party_services` and `package_dependencies`
  - improved control, but added lower-value package-risk noise from structural `package_health` flags
  - not promoted as the live baseline

- `20260411T223329Z-macu-residence-deep`
  - report `f0b7f6e7-7ea2-4758-9b36-292878ee380f`
  - `quality_score=88`
  - first report where weak governance/support sections were more aggressively suppressed instead of expanded into filler

- `20260411T230533Z-macu-residence-deep`
  - report `d397512c-8942-4a8b-8816-5e14667fef02`
  - `quality_score=96`
  - first strong pass of the curated C-suite section set
  - improved deterministic body control, but the cover page and appendix still looked too system-generated

- `20260411T231214Z-macu-residence-deep`
  - report `d5009be5-d1a6-4675-aed8-0ca5a66eb4fa`
  - `quality_score=100`
  - cleaner appendix structure and deduplicated package-version notes
  - still retained a timestamped title and some duplicated metric phrasing in the opening sections

- `20260411T231704Z-macu-residence-deep`
  - report `b2807075-1afe-4de1-bd70-21e8a69ec071`
  - `quality_score=100`
  - current best deployed run
  - title now renders as `Software Architecture Baseline`
  - opening pages are more descriptive and less obviously system-generated
  - recommendations remain absent when disabled

## Current Recommendation

Use the curated descriptive worker behavior from `ent-20260411-191306` as the baseline.

Reason:

- It keeps the stronger trust boundary that earlier remediation introduced.
- It removes more of the obvious machine-shaped framing from the cover page, opening sections, and appendices.
- The curated section set is better aligned with a one-repository C-suite baseline and avoids low-value sections that previously made the document feel auto-generated.
- Remaining issues are now primarily editorial polish, not factual trust or pipeline integrity.

## Current Live Worker Tag

- `ent-20260411-191306`

## Remaining Issues

- The opening summary is still more repository-metric-led than a strong human-authored baseline would be.
- Several section titles are inherited from the long-form template and are broader than the concise descriptive content now emitted under them.
- The appendices are cleaner, but they still read like internal evidence summaries rather than client-ready appendix prose.
- The live benchmark still occasionally records post-rollout transient gRPC dial failures that should be classified separately from content regressions.

## Suggested Next Moves

1. Add a stronger document-level editorial pass for the opening pages so executive text is less metric-led and more narrative while staying descriptive.

2. Review the `Architecture Baseline` section labels and either rename or collapse the ones that still sound broader than the evidence-backed content beneath them.

3. Improve appendix rendering from internal evidence snippets toward more polished appendix summaries.

4. Update the benchmark harness to separate:
   - transient worker-unavailable deployment race failures
   - content-quality regressions

## Validation Commands Used

- `go test ./internal/reports ./internal/api ./persistence`
- overlay worker pytest:
  - `PYTHONPATH="$tmpdir:/Users/jaystuart/dev/sourcebridge" .venv/bin/python -m pytest ...`
- live benchmark:
  - `make benchmark-report-quality-live REPORT_BASE_URL=https://sourcebridge-enterprise.xmojo.net REPORT_REPO_NAME='MACU Residence' REPORT_RESULTS_DIR=benchmarks/results/report-quality-live`
