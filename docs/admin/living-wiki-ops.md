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

## Plan preview before Build

When an operator clicks **Build Overview**, **Build Detailed**, **Regenerate**,
or **Retry** on the repository's Living Wiki settings page, a plan preview modal
opens before the job is enqueued. The modal shows the exact pages that would be
generated for this run so the operator can review the list, deselect optional
pages, and confirm their intent.

### What the preview shows

The modal groups pages into three sections:

| Group | Included pages |
|---|---|
| **Repository pages** | Always generated — `api_reference`, `system_overview`, `glossary`. Cannot be deselected. |
| **Subsystem pages** | One page per detected code cluster (Detailed mode only). |
| **Package pages** | One page per top-level directory, rendered when no clusters are detected. |

The mode pill in the header (Detailed / Overview) carries a tooltip explaining
the generation scope for the selected mode.

### `previewLivingWikiPlan` query

The preview is backed by a synchronous GraphQL query:

```graphql
query {
  previewLivingWikiPlan(
    repositoryId: "repo:abc123"
    mode: DETAILED
    pageCountOverride: 10   # optional; mirrors the per-run override
  ) {
    planSignature
    mode
    modeTooltip
    totalPages
    preCap
    capSource
    capValue
    notice
    pages {
      id
      templateId
      title
      pageType   # REPO_WIDE | ARCHITECTURE | TOP_LEVEL_DIR
      subsystem
      audience
      required
    }
  }
}
```

The query resolves the same taxonomy the cold-start runner would use. Cost is
50–200 ms per call; no caching is applied (every modal open refetches).

When `notice` is non-null and `totalPages` is 0, the system is paused (kill-switch
or globally-disabled) — the modal shows the notice instead of the page list.

### Selection contract

`selectedPageIds` uses nullable-list semantics:

| Value | Meaning |
|---|---|
| `null` / omitted | No filter — build the full plan (today's default). |
| `[]` empty array | Explicit empty selection — build only the 3 repo-wide pages. |
| `["id1", "id2"]` | Explicit selection — build repo-wide pages plus the listed IDs. |

Repository-wide pages (`api_reference`, `system_overview`, `glossary`) are always
generated regardless of what is in `selectedPageIds`.

### Plan signature and staleness protection

`previewLivingWikiPlan` returns a `planSignature` — a SHA-256 hash over the
sorted page IDs, the generation mode, and the effective page cap. When
`selectedPageIds` is supplied to `enableLivingWikiForRepo` or `retryLivingWikiJob`,
`planSignature` is required and must match the current plan.

If the plan has changed between preview and Build (e.g. a re-index added a cluster,
or `MaxPagesPerJob` was changed in another tab), the mutation rejects with:

```json
{
  "errors": [{
    "message": "Living Wiki plan has changed since preview; re-review and try again",
    "extensions": {
      "code": "LIVING_WIKI_PLAN_STALE",
      "freshPlan": { ... }
    }
  }]
}
```

The fresh plan is embedded in `extensions.freshPlan` so the UI can re-render
the modal in a single round-trip. The user's existing deselections are preserved
for any page IDs that still appear in the fresh plan; removed pages are silently
dropped; new pages default to checked. The Build button is re-disabled until the
user interacts with at least one checkbox, forcing explicit re-confirmation.

### When the modal does NOT appear

The preview modal is skipped on two paths:

- **`retryExcludedOnly: true`** (Retry excluded pages CTA) — the page set is
  already explicitly determined from the previous job's `excluded_page_ids`. No
  modal, no signature validation.
- **`ALL_ENABLED` mode** — fires two separate cold-start jobs (Overview + Detailed)
  and is outside the scope of per-mode preview. The `previewLivingWikiPlan` query
  rejects `mode: ALL_ENABLED` with extension code `PREVIEW_MODE_NOT_SUPPORTED`.

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
