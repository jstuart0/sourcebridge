# SourceBridge Release Process

This document describes the steps to cut a SourceBridge release. All items must
be checked before a release tag is pushed.

## Pre-flight checklist

- [ ] `make ci` passes cleanly (lint + all tests)
- [ ] `go vet ./...` clean
- [ ] No open SEV-1 / SEV-2 issues on main
- [ ] CHANGELOG.md updated with release notes
- [ ] Version constant in `cli/version.go` (or equivalent) bumped
- [ ] Docker images build cleanly for `linux/amd64` and `linux/arm64`

## Deploying to production (thor cluster)

```bash
# 1. Build and push images
make docker-build
docker push YOUR_REGISTRY/sourcebridge:vX.Y.Z

# 2. Update image tags in manifests or Helm values
# 3. Apply with kustomize or helm upgrade
kubectl -n sourcebridge set image deployment/sourcebridge-api \
    sourcebridge-api=YOUR_REGISTRY/sourcebridge:vX.Y.Z
kubectl -n sourcebridge rollout status deployment/sourcebridge-api

# 4. Verify pods are healthy
kubectl -n sourcebridge get pods
```

## Living Wiki release validation

**This section must be completed for any release that touches:**
- `internal/livingwiki/`
- `internal/settings/livingwiki/`
- `internal/db/livingwiki_*`
- `web/src/app/(app)/repositories/[id]/page.tsx`
- `web/src/app/(app)/settings/living-wiki/page.tsx`
- `cmd/livingwiki-smoke/`

All eight steps below must be verified. Check each box and fill in the
observed result. Attach the activity-feed screenshot to the release PR.

### Tier 1 — Unit-integration test (must pass before release PR is merged)

- [ ] `go test -tags integration ./internal/livingwiki/... -v -run ^TestLivingWikiE2E` passes
  - Run: `make test-livingwiki-integration`
  - Expected: all sub-tests pass, no race conditions (`-race` flag is applied)
  - Failure mode to look for: assertion failures on PagesGenerated count or
    JobResult.Status — indicates a regression in orchestrator/store wiring

### Tier 2 — Real-Confluence smoke (must pass within 48 hours of release)

- [ ] Weekly CronJob has run at least once since the last deploy and its result
      appears in the SourceBridge admin job view (or Slack channel if configured)
  - Trigger manually if the weekly schedule has not fired:
    ```bash
    kubectl -n sourcebridge create job \
        --from=cronjob/livingwiki-smoke \
        livingwiki-smoke-release-$(date +%s)
    kubectl -n sourcebridge logs -l app.kubernetes.io/name=livingwiki-smoke --tail=100
    ```
  - Acceptance: final log line contains `living-wiki smoke PASSED`
  - Failure mode: `status=failed` or `pages_generated=0` — check Confluence
    credentials are still valid and the test repo is indexed

### Tier 3 — Manual release validation (8-step user scenario)

Run these steps on the staging or production instance immediately after deploy.
Each step has a pass criterion and the specific failure mode to watch for.

#### Step 1 — Install and index

- [ ] Install SourceBridge (Docker Compose or cluster deploy)
- [ ] Add a repository via Settings → Repositories
- [ ] Wait for indexing to complete (progress bar reaches 100%, status = Indexed)
- Pass criterion: Repository appears in list with status "Indexed"
- Failure mode: Indexing stuck at 0% → check worker pod logs for gRPC errors

#### Step 2 — Configure Confluence credentials

- [ ] Open Settings → Living Wiki
- [ ] Enter Confluence URL, email, and API token
- [ ] Click "Test Connection" — expect "Connection successful"
- Pass criterion: Toast or inline message shows "Connection successful"
- Failure mode: "401 Unauthorized" → token is wrong; "connection refused" →
  Confluence URL is unreachable from the cluster

#### Step 3 — Open per-repo settings and see activation gate (Stage A)

- [ ] Open the indexed repository's Settings tab
- [ ] Scroll to the Living Wiki panel
- [ ] Confirm the panel shows State 1 (activation gate with mode + sink selector)
- [ ] Verify Confluence sink row is visible (even if credentials were skipped)
- Pass criterion: Mode selector and at least one sink row visible within 2 seconds
- Failure mode: Panel shows State 0 ("Living Wiki is disabled globally") → global
  enabled flag is false; go to Settings → Living Wiki and enable it

#### Step 4 — Enable Living Wiki (cold-start)

- [ ] Select "Confluence" as the sink (audience: Engineer)
- [ ] Click "Enable Living Wiki"
- [ ] Panel transitions to State 3 (progress bar visible)
- [ ] Progress bar is determinate (shows "N of M pages" immediately, not a spinner)
- Pass criterion: Progress bar visible within 3 seconds; phase message updates
  as pages complete; bar reaches 100% within the expected time for the repo size
- Failure mode: Button spins indefinitely without progress → check that the
  dispatcher is started (`grep "living-wiki dispatcher started"` in API pod logs)

#### Step 5 — Verify Confluence pages

- [ ] After job completes, panel transitions to State 4 (Enabled, success)
- [ ] Open Confluence and navigate to the target space
- [ ] Confirm N pages exist with SourceBridge frontmatter
  (look for `<!-- sourcebridge_page_id:` comment in page source)
- Pass criterion: At least one page per top-level package in the repo, plus
  api_reference, system_overview, and glossary pages
- Failure mode: Zero pages in Confluence → check job result in admin view for
  FailureCategory; "auth" means credentials expired mid-job

#### Step 6 — Trigger regen via commit

- [ ] Commit any change to the indexed repository (e.g., update a comment)
- [ ] Wait one scheduler interval (default 15 minutes) or trigger manually:
  ```bash
  # If push webhooks are configured:
  # Push the commit and watch the dispatcher logs.
  # If using the periodic scheduler:
  # Wait 15 minutes or set SOURCEBRIDGE_LIVING_WIKI_SCHEDULER_INTERVAL=1m
  # for a faster cycle.
  ```
- [ ] Verify the activity feed shows a new living-wiki job entry
- Pass criterion: New job appears in activity feed with status "done"; Confluence
  page "Last modified" timestamp updates
- Failure mode: No new job after 2x scheduler interval → check scheduler log
  (`grep "living-wiki scheduler: submitted refresh events"` in API pod logs)

#### Step 7 — Human edit preservation

- [ ] Edit one paragraph in a SourceBridge-generated Confluence page manually
- [ ] Wait for the next regen pass (or trigger manually)
- [ ] Open the same Confluence page — confirm the human edit is preserved
- Pass criterion: Human-edited paragraph content unchanged after regen
- Failure mode: Human edit overwritten → SourceBridge block ownership tracking
  is not working; check `ast.OwnerHumanEdited` blocks in the page's AST store

#### Step 8 — Disable living wiki

- [ ] Open the repository's Settings tab → Living Wiki panel (State 4)
- [ ] Click "Disable"
- [ ] Confirm the dialog text describes what will happen to existing pages
  (text should include "Existing pages in Confluence will remain")
- [ ] Click "Disable" in the dialog
- [ ] Panel returns to State 1 (activation gate)
- [ ] Wait one scheduler interval — confirm NO new regen job appears
- [ ] Open Confluence — confirm existing pages are still present and have a
  "no longer auto-managed" banner at the top
- Pass criterion: No new jobs after disable; pages intact with stale banner
- Failure mode: Pages deleted → destructive disable was triggered incorrectly
  (should never happen; the disable path is soft-disable only)

### Rollback procedure

If any Tier 2 or Tier 3 step fails after deploy:

1. Check API pod logs for the specific error class:
   - `FAILED_AUTH` → Confluence credentials rotated; update in Settings → Living Wiki
   - `ErrTimeBudgetExceeded` → Large repo; increase `SOURCEBRIDGE_LIVING_WIKI_JOB_TIMEOUT`
   - Dispatcher goroutines not running → Set `SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH=true`
     to pause the feature without a rollback

2. To roll back the API deployment:
   ```bash
   kubectl -n sourcebridge rollout undo deployment/sourcebridge-api
   kubectl -n sourcebridge rollout status deployment/sourcebridge-api
   ```

3. DB migrations (036) are forward-compatible. Rolling back the binary does not
   require reverting the migration — the new tables are ignored by the old binary.

### Release sign-off

Before tagging the release:

- [ ] All Tier 1 tests pass (`make test-livingwiki-integration`)
- [ ] Tier 2 smoke passed (attach job log URL or Slack screenshot)
- [ ] All 8 Tier 3 steps verified with pass results noted above
- [ ] Rollback procedure tested (optional but recommended for major versions)

Reviewer: confirm all checkboxes are checked before approving the release PR.
