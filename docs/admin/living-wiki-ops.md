# Living Wiki Operator Guide

This document covers operational concerns for Living Wiki: page-count behavior,
the MaxPagesPerJob setting, the per-run override, and the targeted-retry path.

For LLM provider and model configuration, see
[`docs/admin/llm-config.md`](llm-config.md).

---

## Page-count behavior

When a Living Wiki cold-start runs, the orchestrator resolves a **planned page
set** from the repository's symbol graph and cluster topology. The size of this
set is determined by three sources:

| Source | Pages |
|---|---|
| Cluster architecture pages | One per cluster (from `resolveClusterArchPages`) |
| Top-level-dir fallback pages | One per top-level directory when no clusters exist, capped at 25 |
| Repo-wide pages | Always 3 fixed: `api_reference`, `system_overview`, `glossary` |

So "9 pages" on a 6-cluster repo is `6 cluster + 3 repo-wide`. No magic
number. The planning log line emitted at the start of every cold-start
captures this breakdown:

```
livingwiki/coldstart: planned page count
  repo_id=<id>  mode=lw_detailed
  cluster_pages=6  top_level_dir_pages=0  repo_wide_pages=3
  pre_cap_total=9  total=9
  cap_source=none  cap_value=0  excluded_only_retry=false
```

The same information is surfaced in the job activity feed:
`"mode=lw_detailed: 6 cluster + 3 repo-wide = 9 pages"`.

---

## Three-tier priority for the page cap

When a cap is in effect, the effective limit is resolved in this order:

1. **Per-run override** (`pageCountOverride` on `retryLivingWikiJob` or
   `enableLivingWikiForRepo`) — highest priority; not persisted.
2. **Repo MaxPagesPerJob** (Settings → Max pages per job) — per-repo setting,
   persisted. Default **500**.
3. **No cap** when neither is set or both are above the planned set size.

The `cap_source` field in the planning log identifies which tier applied:
`"per_run_override"`, `"repo_setting"`, or `"none"`.

---

## MaxPagesPerJob

**MaxPagesPerJob** is a per-repo setting that caps pages generated on every
cold-start and every scheduled run.

- Default: **500** (effective no-op for typical repos; cluster+repo-wide pages
  on realistic codebases rarely exceed 100).
- Change via Settings → "Max pages per job" → Save changes.
- Valid range: 1–500 (matches the UI input bounds).
- If the planned set exceeds the cap, repo-wide pages (always 3) are retained
  and architecture/top-level-dir pages are truncated in stable order.

> **Note (CA-146)**: `MaxPagesPerJob` was previously stored in the database
> but never applied at runtime. It is wired as a real cap as of CA-146. Repos
> whose database value was `50` (the prior default) had that value migrated
> to `500` automatically. Repos where an operator deliberately set a value
> other than `50` retain their custom value.

---

## Per-run page count override

To cap a single run without changing the persisted setting, pass
`pageCountOverride` (integer, 1–500) to either mutation:

```graphql
mutation {
  retryLivingWikiJob(
    repositoryId: "repo:abc123"
    mode: DETAILED
    pageCountOverride: 10
  ) {
    jobId
    notice
  }
}
```

Or via the UI: expand **Run options** below the Build buttons and set
"Page count override". The value is cleared automatically after the run
completes (one-shot semantic).

Validation: values outside 1–500 return a GraphQL error with extension code
`LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE`.

---

## Targeted-retry exemption

When `retryExcludedOnly: true` is passed (the "Retry excluded pages" CTA),
the runner regenerates exactly the pages named in the prior job's
`excluded_page_ids`. **The cap is not applied on this path.** Capping would
silently discard caller-requested work.

The planning log records `excluded_only_retry=true` and `cap_source=none`
for these runs so the reason for skipping the cap is unambiguous.

---

## Verifying page-count behavior from logs

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=500 \
  | grep "planned page count"
```

Key fields to check:

| Field | Meaning |
|---|---|
| `cluster_pages` | Architecture pages from cluster topology |
| `top_level_dir_pages` | Fallback pages when no clusters present |
| `repo_wide_pages` | Always 3 (api_reference, system_overview, glossary) |
| `pre_cap_total` | Planned count before any cap was applied |
| `total` | Final count sent to the generator |
| `cap_source` | `none` / `repo_setting` / `per_run_override` |
| `cap_value` | Effective cap (0 when cap_source=none) |
| `excluded_only_retry` | true when cap was bypassed for targeted retry |
