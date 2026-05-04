# CODEAWARE Legacy-Name Census

**Generated**: 2026-05-04T21:28:33Z
**Commit**: `453cc16`
**Total references**: 106
**Search root**: `/Users/jaystuart/dev/sourcebridge`

This census is input for Phase 4 Slice NAME-2 of the system-audit-refactor
campaign. Each reference should be triaged as one of:

- **KEEP** — still consumed by deployed infra (renaming would break prod)
- **DEFER** — DB table name, persisted config key, k8s resource name, or archived doc
- **RENAME** — safe to rename (internal var, CSS class, comment)

## Bulk Triage

| Category | Rule | Examples |
|----------|------|---------|
| **KEEP** | `CODEAWARE_*` env vars read by running Go services | `os.Getenv("CODEAWARE_…")` in `internal/` |
| **DEFER** | `codeaware` k8s namespace (SurrealDB lives there — cross-service DNS) | `kubectl -n codeaware` in plans |
| **DEFER** | `ca_llm_config`, `ca_api_token` DB/SurrealDB table names | schema, migrations |
| **DEFER** | `thoughts/` plan/audit files — historical context, not executable code | all `thoughts/shared/plans/*.md` |
| **DEFER** | `scripts/audit-legacy-names.sh` itself — grep pattern must reference the old name | line 90, 122, 130, 173, 175 |
| **RENAME** | `ca-shell`, `ca-shell-grid`, `ca-loading-spinner`, `ca-spin` CSS classes | **Done in this commit** |
| **RENAME** | `CLAUDE.md` prose references to "CodeAware" name | safe to update text |

> CSS class names (`ca-shell`, `ca-shell-grid`, `ca-loading-spinner`, `@keyframes ca-spin`) have been renamed to `sb-*` in this commit (Phase 4 Slice 11). The remaining 106 references are either KEEP, DEFER, or documentation prose with no runtime impact.

---

| File | Line | Context |
|------|------|---------|
| \`.claude/worktrees/agent-a520b83481bbe0ee2/CLAUDE.md\` | 73 | \`## Legacy Name: CodeAware\` |
| \`.claude/worktrees/agent-a520b83481bbe0ee2/CLAUDE.md\` | 75 | \`This project was originally called **CodeAware**. It has since been renamed to **SourceBridge**, but remnants of the ...\` |
| \`.claude/worktrees/agent-a520b83481bbe0ee2/CLAUDE.md\` | 77 | \`When you encounter a \`CODEAWARE_\` or \`codeaware\` reference:\` |
| \`.claude/worktrees/agent-a69da81a09d6a4aba/CLAUDE.md\` | 73 | \`## Legacy Name: CodeAware\` |
| \`.claude/worktrees/agent-a69da81a09d6a4aba/CLAUDE.md\` | 75 | \`This project was originally called **CodeAware**. It has since been renamed to **SourceBridge**, but remnants of the ...\` |
| \`.claude/worktrees/agent-a69da81a09d6a4aba/CLAUDE.md\` | 77 | \`When you encounter a \`CODEAWARE_\` or \`codeaware\` reference:\` |
| \`.claude/worktrees/agent-aba8c592713c35570/CLAUDE.md\` | 73 | \`## Legacy Name: CodeAware\` |
| \`.claude/worktrees/agent-aba8c592713c35570/CLAUDE.md\` | 75 | \`This project was originally called **CodeAware**. It has since been renamed to **SourceBridge**, but remnants of the ...\` |
| \`.claude/worktrees/agent-aba8c592713c35570/CLAUDE.md\` | 77 | \`When you encounter a \`CODEAWARE_\` or \`codeaware\` reference:\` |
| \`.claude/worktrees/agent-acdd7740f68b5bdab/CLAUDE.md\` | 73 | \`## Legacy Name: CodeAware\` |
| \`.claude/worktrees/agent-acdd7740f68b5bdab/CLAUDE.md\` | 75 | \`This project was originally called **CodeAware**. It has since been renamed to **SourceBridge**, but remnants of the ...\` |
| \`.claude/worktrees/agent-acdd7740f68b5bdab/CLAUDE.md\` | 77 | \`When you encounter a \`CODEAWARE_\` or \`codeaware\` reference:\` |
| \`CLAUDE.md\` | 82 | \`## Legacy Name: CodeAware\` |
| \`CLAUDE.md\` | 84 | \`This project was originally called **CodeAware**. It has since been renamed to **SourceBridge**, but remnants of the ...\` |
| \`CLAUDE.md\` | 86 | \`When you encounter a \`CODEAWARE_\` or \`codeaware\` reference:\` |
| \`CLAUDE.md\` | 101 | \`- Project Name: CodeAware\` |
| \`scripts/audit-legacy-names.sh\` | 2 | \`# audit-legacy-names.sh — census of CODEAWARE_* / codeaware references.\` |
| \`scripts/audit-legacy-names.sh\` | 11 | \`#   Prints a markdown table (or TSV/plain) of every remaining CODEAWARE_* and\` |
| \`scripts/audit-legacy-names.sh\` | 12 | \`#   codeaware reference with file:line and a short context snippet.\` |
| \`scripts/audit-legacy-names.sh\` | 14 | \`#       scripts/audit-legacy-names.sh > /tmp/codeaware-census.md\` |
| \`scripts/audit-legacy-names.sh\` | 17 | \`#   This census drives the CODEAWARE→SOURCEBRIDGE rename campaign.\` |
| \`scripts/audit-legacy-names.sh\` | 90 | \`grepout="$(grep -n -i "CODEAWARE\\|codeaware" "$f" 2>/dev/null)" \|\| true\` |
| \`scripts/audit-legacy-names.sh\` | 122 | \`echo "# CODEAWARE legacy-name census — $GENERATED_AT (sha: $GIT_SHA)"\` |
| \`scripts/audit-legacy-names.sh\` | 130 | \`# CODEAWARE Legacy-Name Census\` |
| \`scripts/audit-legacy-names.sh\` | 173 | \`- \\`CODEAWARE_*\\` environment variables **must not** be removed; they are\` |
| \`scripts/audit-legacy-names.sh\` | 175 | \`- Database table names (\\`codeaware_*\\`) are out of scope for this campaign.\` |
| \`scripts/check-no-public-removals.sh\` | 29 | \`#   R8  Environment-variable read deleted             SOURCEBRIDGE_* CODEAWARE_*\` |
| \`scripts/check-no-public-removals.sh\` | 194 | \`section "R8: Env-var read deleted (SOURCEBRIDGE_* / CODEAWARE_*)"\` |
| \`scripts/check-no-public-removals.sh\` | 202 | \`done < <(echo "$fillediff" \| grep -E '^-[[:space:]]*[a-zA-Z_]+[[:space:]]*:?=[[:space:]]*os\.Getenv\("(SOURCEBRIDGE\|C...\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.bob.md\` | 166 | \`### A-L1: \`CODEAWARE_*\` legacy env var references stranded without migration path or census\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.bob.md\` | 170 | \`CLAUDE.md documents intentional dual-branding but no register of what's been left. Operators set \`CODEAWARE_*\` from o...\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.bob.md\` | 172 | \`**Fix:** Add \`scripts/audit-legacy-names.sh\` greping \`CODEAWARE_\` and printing file:line. Run once for census. For ea...\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.md\` | 34 | \`4. **CodeAware legacy naming sprawl** — ruby (M-1: \`ca-\` CSS classes), bob (A-L1: \`CODEAWARE_*\` env vars), implicit i...\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.md\` | 176 | \`### Theme 13: CodeAware legacy naming (ruby + bob)\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.md\` | 181 | \`\| **NAME-2** (bob A-L1) \| Low \| S \| \`CODEAWARE_*\` env var references stranded — generate census via \`scripts/audit-le...\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.ruby.md\` | 105 | \`### M-1: \`ca-\` CSS class prefix is a CodeAware legacy name\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.ruby.md\` | 109 | \`\`ca-shell\`, \`ca-shell-grid\`, \`ca-loading-spinner\` use old CodeAware brand. Per CLAUDE.md "if you can safely replace i...\` |
| \`thoughts/shared/audits/2026-05-04-system-audit-refactor.ruby.md\` | 235 | \`\| M-1 \| Medium \| \`ca-\` legacy CodeAware brand prefix \|\` |
| \`thoughts/shared/investigations/2026-04-30-tester-report-pazaryna.md\` | 249 | \`1. **Plane project**: confirm \`CodeAware (CA)\` vs \`CodeAware Enterprise (CE)\` for tracking. (Investigation assumed \`C...\` |
| \`thoughts/shared/plans/2026-04-19-audit-remediation.md\` | 43 | \`- **Versioning debt**: CodeAware → SourceBridge rename ~90% done;\` |
| \`thoughts/shared/plans/2026-04-19-audit-remediation.md\` | 596 | \`- \`CODEAWARE_\` env prefix — log deprecation warning; remove in a\` |
| \`thoughts/shared/plans/2026-04-19-audit-remediation.md\` | 598 | \`- \`# CodeAware GraphQL Schema\` header rename.\` |
| \`thoughts/shared/plans/2026-04-19-audit-remediation.md\` | 734 | \`- Full removal of \`CODEAWARE_\` env fallback — deprecation logs this\` |
| \`thoughts/shared/plans/2026-04-29-deep-cliffnotes-deadline-exceeded.state.md\` | 31 | \`- Name: CodeAware (legacy name; project is the SourceBridge OSS repo)\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-carryover-five.md\` | 1120 | \`#   kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-carryover-five.md\` | 1124 | \`#   kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-carryover-five.md\` | 1134 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.codex-r1-plan.md\` | 65 | \`\`CLAUDE.md\` requires explicit namespaces for \`kubectl\`, which the checklist does, but the checklist alternates betwee...\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.codex-r1b-plan.md\` | 17 | \`\| Low: smoke checklist namespace drift \| **Addressed.** The plan now documents \`sourcebridge\` for API/worker and \`cod...\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.codex-r1c-plan.md\` | 17 | \`\| r1 Low: smoke checklist namespace drift \| **Addressed.** The namespace map is explicit: \`sourcebridge\` for API/work...\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.md\` | 550 | \`- SurrealDB pod lives in **\`codeaware\`** namespace, exposed to \`sourcebridge\` via an ExternalName Service alias. So \`...\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.md\` | 558 | \`kubectl -n codeaware get pods      # surrealdb\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.md\` | 570 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.md\` | 589 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.md\` | 597 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-cold-start-progress.md\` | 605 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.codex-r1-plan.md\` | 110 | \`CLAUDE.md says not to rename persisted database table names that deployed infrastructure depends on. The plan mostly ...\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.codex-r1-plan.md\` | 112 | \`Concrete fix: replace every \`livingwiki_repo_settings\` reference with \`lw_repo_settings\`. No fix needed for existing ...\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md\` | 575 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md\` | 592 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md\` | 612 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md\` | 922 | \`kubectl -n codeaware exec -it surrealdb-0 -- /surreal sql --conn http://localhost:8000 \\` |
| \`thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md\` | 2019 | \`All \`lw_repo_settings\` references in the plan are corrected to \`lw_repo_settings\` (CR10 already locks the migration; ...\` |
| \`thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.state.md\` | 22 | \`- Plane ticket: not configured for SourceBridge — two \`CodeAware*\` projects exist in the \`agile-solutions-group\` work...\` |
| \`thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.state.md\` | 172 | \`**Plane ticket: skipped.** Two \`CodeAware*\` Plane projects exist in\` |
| \`thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.state.md\` | 199 | \`- Plane project resolution (which CodeAware* project, plus\` |
| \`thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.codex-r1-plan.md\` | 4 | \`- \`CLAUDE.md\`, especially the legacy CodeAware caveat and configuration rules.\` |
| \`thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.codex-r1-plan.md\` | 110 | \`The plan correctly avoids renaming \`ca_llm_config\` and calls out the legacy CodeAware naming caveat. Keep that postur...\` |
| \`thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.md\` | 79 | \`- Do not rename \`ca_llm_config\` table or \`CODEAWARE_*\` env vars (CLAUDE.md legacy-name caveat).\` |
| \`thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.md\` | 503 | \`- \`/Users/jaystuart/dev/sourcebridge/CLAUDE.md\` — legacy \`CODEAWARE_*\` naming caveat\` |
| \`thoughts/shared/plans/2026-04-30-tester-report-pazaryna.state.md\` | 17 | \`## Tickets (Plane: CodeAware / CA)\` |
| \`thoughts/shared/plans/2026-05-01-ca-136-deferred-followups.state.md\` | 27 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-01-ca128-ci-greenup.state.md\` | 20 | \`- Plane project: d3fa4bd8-1177-4364-88a7-aae69698b75d (CodeAware / CA)\` |
| \`thoughts/shared/plans/2026-05-01-deploy-pipeline-overhaul-wave2.state.md\` | 53 | \`d3fa4bd8-1177-4364-88a7-aae69698b75d (CodeAware)\` |
| \`thoughts/shared/plans/2026-05-01-deploy-pipeline-overhaul-wave3.md\` | 120 | \`- Migrating surrealdb StatefulSet from \`codeaware\` namespace into \`sourcebridge\` (mentioned in \`surrealdb-service-pat...\` |
| \`thoughts/shared/plans/2026-05-01-deploy-pipeline-overhaul-wave3.state.md\` | 55 | \`d3fa4bd8-1177-4364-88a7-aae69698b75d (CodeAware) — even though this is k8s-home-lab repo, the parent CA-129 epic is i...\` |
| \`thoughts/shared/plans/2026-05-01-llm-profile-picker-broken-deployed.state.md\` | 24 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-01-version-display-and-flaky-test-fix.state.md\` | 24 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-02-ca-107-discusscode-symbol-source.state.md\` | 33 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-02-ca-138-ca-147-continuation.state.md\` | 42 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-02-ca-146-plan-preview-ux.state.md\` | 35 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-02-ca-60-field-guide-default-landing.state.md\` | 33 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-02-ca-61-first-run-funnel-telemetry.md\` | 4 | \`**Ticket**: CA-61 (CodeAware project)\` |
| \`thoughts/shared/plans/2026-05-02-ca-61-first-run-funnel-telemetry.state.md\` | 33 | \`- Project Name: CodeAware\` |
| \`thoughts/shared/plans/2026-05-02-living-wiki-cold-start-remediation.flow.md\` | 12 | \`\| Plane project \| d3fa4bd8-1177-4364-88a7-aae69698b75d (CodeAware) \|\` |
| \`thoughts/shared/plans/2026-05-02-living-wiki-cold-start-remediation.state.md\` | 45 | \`- Plane project: d3fa4bd8-1177-4364-88a7-aae69698b75d (CodeAware)\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 22 | \`5. Any **CLI flag**, **config key**, or **environment variable** (including \`CODEAWARE_*\`).\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 99 | \`- Database table renames (the existing \`codeaware_*\` table names that exist in DB schema stay — see CLAUDE.md "Legacy...\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 100 | \`- \`CODEAWARE_*\` environment variables stay readable as fallbacks; only **internal CSS class names** (e.g. \`ca-shell\`)...\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 205 | \`- **Env-var reads** — lines matching \`^-\s*[a-z_]+\s*=\s*os\.Getenv\("(SOURCEBRIDGE\|CODEAWARE)_\`.\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 211 | \`- \`audit-legacy-names.sh\` greps \`CODEAWARE_\` and \`codeaware\` across the repo and prints \`file:line:context\` for the N...\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 324 | \`- New migration in \`internal/graph/surrealdb_migrations/\` that adds \`role\` to the existing **\`ca_api_token\`** Surreal...\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 1039 | \`**Goal**: Reduce \`RepositoryDetailPage\` from 3,836 lines to a thin tab-router (~300 lines target) by extracting per-t...\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 1401 | \`- new: \`docs/codeaware-legacy-census.md\` (the audit output of \`scripts/audit-legacy-names.sh\`)\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 1406 | \`- **NAME-2**: run \`scripts/audit-legacy-names.sh\`. Generate \`docs/codeaware-legacy-census.md\` listing every \`CODEAWAR...\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 1669 | \`- Add a "Legacy CodeAware naming" pointer to \`docs/codeaware-legacy-census.md\` (Phase 4 Slice 11 outcome).\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 1677 | \`- **\`docs/codeaware-legacy-census.md\`** (new): Phase 4 Slice 11 output.\` |
| \`thoughts/shared/plans/2026-05-04-system-audit-refactor.md\` | 1700 | \`- Project guidance: \`/Users/jaystuart/dev/sourcebridge/CLAUDE.md\` (renamed-from-CodeAware policy)\` |
| \`thoughts/shared/plans/CA-141-stale-job-reaper-lag.md\` | 6 | \`\| Plane project \| \`d3fa4bd8-1177-4364-88a7-aae69698b75d\` (CodeAware) \|\` |
| \`thoughts/shared/plans/CA-142-argo-image-updater-inflight-jobs.codex-r1-plan.md\` | 181 | \`- The plan respects the SourceBridge/CodeAware naming context and does not propose unsafe runtime renames.\` |
| \`thoughts/shared/plans/CA-142-argo-image-updater-inflight-jobs.md\` | 6 | \`\| Plane project \| d3fa4bd8-1177-4364-88a7-aae69698b75d (CodeAware) \|\` |
| \`thoughts/shared/plans/CA-144-per-page-progress-signal.md\` | 6 | \`\| Plane project \| \`d3fa4bd8-1177-4364-88a7-aae69698b75d\` (CodeAware) \|\` |
| \`thoughts/shared/plans/CA-145-CA-143-progress-counter-and-retry-resume.md\` | 6 | \`\| Plane project \| \`d3fa4bd8-1177-4364-88a7-aae69698b75d\` (CodeAware) \|\` |
| \`thoughts/shared/plans/CA-146-lw-detailed-page-count.md\` | 6 | \`\| Plane project \| \`d3fa4bd8-1177-4364-88a7-aae69698b75d\` (CodeAware) \|\` |
| \`thoughts/shared/plans/CA-150-quality-gate-provider-profiles.md\` | 6 | \`\| Plane project \| d3fa4bd8-1177-4364-88a7-aae69698b75d (CodeAware) \|\` |
| \`thoughts/shared/plans/CA-150-quality-gate-provider-profiles.md\` | 759 | \`- **Renaming any persisted resource** (per CLAUDE.md \`CodeAware\` legacy guidance) —\` |

---

## Notes

- `CODEAWARE_*` environment variables **must not** be removed; they are
  still read as fallbacks by deployed infrastructure (see CLAUDE.md §Legacy Name).
- Database table names (`codeaware_*`) are out of scope for this campaign.
- Internal Go variable names, CSS class names, and comment text are eligible
  for rename when the same commit removes all callers.

_Generated by `scripts/audit-legacy-names.sh` — re-run at any time for a fresh count._
