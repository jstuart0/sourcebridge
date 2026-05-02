# SourceBridge Release Process

SourceBridge uses [release-please](https://github.com/googleapis/release-please)
to automate version bumping and changelog generation. Most releases require
zero manual intervention: write conventional-commit PRs, merge them, then
merge the Release PR release-please opens.

## Standard release flow (release-please-driven)

1. **Write conventional-commit PRs.** PR titles and squash-merge messages
   follow `<type>(<scope>): <subject>` (e.g. `feat(ca-138): extend GraphQL
   VersionInfo`). Allowed types and the changelog section they map to:

   | Type | CHANGELOG section | Notes |
   |---|---|---|
   | `feat` | Added | Triggers a minor bump |
   | `fix` | Fixed | Triggers a patch bump |
   | `perf` | Changed | Patch bump |
   | `refactor` | Changed | Patch bump |
   | `docs` | Documentation | No version bump |
   | `deps` | Changed | Patch bump |
   | `revert` | Removed | Triggers next-greater-than-reverted bump |
   | `build` / `ci` / `test` / `chore` | Changed (hidden) | No section in CHANGELOG |

   `feat!:` or any commit with a `BREAKING CHANGE:` footer triggers a
   major bump. While we're on 0.x (per `bump-minor-pre-major: true`),
   breaking changes promote the minor instead — same effect.

2. **release-please opens (or updates) a Release PR** every time `main`
   advances. The PR shows:
   - The next version computed from accumulated conventional commits
     since the last release.
   - The auto-generated CHANGELOG diff (Keep-a-Changelog format via
     `changelog-sections` config).

3. **Review the Release PR.** Spot-check the version, the CHANGELOG, and
   any version-tracked manifests. If a release should carry a narrative
   beyond the mechanical CHANGELOG, write it now in the GitHub Release
   body — release-please writes the auto-generated CHANGELOG; the human
   story lives in the Release body.

4. **Run Living-Wiki Tier 1/2/3 validation** (below) if the release
   touches `internal/livingwiki/`.

5. **Merge the Release PR.** This creates the tag (e.g. `v0.10.0`).

   **If `RELEASE_PLEASE_TOKEN` is provisioned** (a GitHub App or PAT —
   see "Provisioning the release-please token" below): the tag push
   automatically triggers `oss-release.yml` (binary build, Docker image
   build, cosign signing per CA-139, GitHub Release publish, Homebrew
   tap update) and `build-images.yml` (component image build + cosign
   signing). Done.

   **If `RELEASE_PLEASE_TOKEN` is NOT yet provisioned** (CA-149 follow-up
   state): GitHub suppresses workflow-on-workflow chains when the
   trigger token is the default `GITHUB_TOKEN`. release-please will
   cut the tag, but downstream workflows will NOT fire automatically.
   **Manual unblock**: re-push the tag from a local checkout with a
   human-credentialled remote:
   ```bash
   git fetch origin --tags
   git push origin :refs/tags/v0.10.0    # delete and re-push
   git push origin v0.10.0
   ```
   Now `oss-release.yml` and `build-images.yml` fire as expected.

6. **Verify the published release.**
   - `cosign verify` the published images per the recipes in
     [`docs/admin/build-info.md`](docs/admin/build-info.md#verifying-signed-images).
   - Confirm the Homebrew tap commit landed.
   - Sanity-check the Release page on GitHub: assets attached,
     SHA256SUMS present, body has both the release-please CHANGELOG
     content and any human narrative.

## VSCode extension release

The `plugins/vscode` directory tracks its own version chain. release-please
opens a separate Release PR with the tag shape `sourcebridge-vscode-vX.Y.Z`,
distinct from the root `vX.Y.Z`. Merge the plugin PR independently of the
main release cadence.

The plugin tags do NOT trigger `oss-release.yml` or `build-images.yml`
(per the SemVer-shaped tag glob in those workflows). The plugin's
publish path is its own (VSCode marketplace), separate from this repo's
binary/image release pipeline.

## Provisioning the release-please token (one-time setup)

Until this is done, manual tag re-push is required after every
release-please merge (see step 5 above). Tracked in CA-149.

**Option A: GitHub App (preferred for organizations)**

1. Create a new GitHub App on the SourceBridge org with these permissions:
   - **Contents**: Read & Write
   - **Pull requests**: Read & Write
   - **Issues**: Read & Write
2. Install the app on the `sourcebridge-ai/sourcebridge` repo only.
3. Generate and download the App's private key.
4. In repo settings → Secrets and variables → Actions, add:
   - `RELEASE_PLEASE_APP_ID` = the app's numeric ID
   - `RELEASE_PLEASE_PRIVATE_KEY` = the private key contents
5. Update `.github/workflows/release-please.yml` to mint a token via
   `actions/create-github-app-token@v1` and pass it as `token:` to the
   release-please action. Replace the
   `secrets.RELEASE_PLEASE_TOKEN || secrets.GITHUB_TOKEN` fallback.

**Option B: Fine-grained Personal Access Token (lighter setup)**

1. As a maintainer, create a fine-grained PAT scoped to the repo with:
   - Contents: Read & Write
   - Pull requests: Read & Write
   - Issues: Read & Write
2. Add as repo secret `RELEASE_PLEASE_TOKEN`.
3. The existing workflow's `secrets.RELEASE_PLEASE_TOKEN ||
   secrets.GITHUB_TOKEN` expression picks it up automatically.

**Why this matters**: tags created with the default `GITHUB_TOKEN` do NOT
trigger downstream `on: push: tags` workflows. Without one of these tokens
provisioned, release-please can cut a tag but `oss-release.yml` and
`build-images.yml` won't fire — meaning no binary publish, no image build,
no cosign signing, no Homebrew tap update.

## Manual fallback

If release-please is broken or you need an out-of-band release:

1. Edit `CHANGELOG.md` manually: move `[Unreleased]` content under a
   new `[vX.Y.Z] - <date>` heading.
2. Update `.release-please-manifest.json` so release-please's next run
   doesn't re-propose the same version: set the package's value to
   `X.Y.Z`.
3. Commit those edits, tag the commit, push the branch and tag
   explicitly (avoid `git push --tags`, which can push unrelated stale
   local tags — this repo has at least one such dangling tag from
   prior PR-merge superseding):

   ```bash
   git commit -m "chore(release): vX.Y.Z"
   git tag vX.Y.Z
   git push origin main          # push the release commit
   git push origin vX.Y.Z        # push only the tag we just created
   ```

4. The existing `oss-release.yml` and `build-images.yml` workflows fire
   on the tag push.

This path also works for prerelease/RC tags. release-please supports
prereleases via PR titles like `chore: release v0.10.0-rc.1`, but the
manual flow is more direct when cadence is non-routine.

## Stale instructions removed

Prior versions of this file mentioned a "version constant in `cli/version.go`
(or equivalent)" that needed bumping. Both have been obsoleted:

- **CA-136** moved the version surface to `internal/version` ldflag-injection;
  `scripts/version.sh` derives the value from git at build time.
- **CA-147** automated tag creation via release-please; the operator no
  longer hand-bumps anything.

If you encounter a copy of these instructions in fork/branch documentation,
delete them — they're misleading.

## Pre-flight checklist (per Release PR)

Before merging a Release PR:

- [ ] `make ci` passes cleanly (lint + all tests) on the latest main commit
- [ ] `go vet ./...` clean
- [ ] No open SEV-1 / SEV-2 issues on main
- [ ] CHANGELOG diff in the Release PR looks correct (release-please-generated)
- [ ] Docker images build cleanly for `linux/amd64` and `linux/arm64`
      (verified by the most recent `build-images.yml` run on main)
- [ ] If touching Living Wiki paths: Living-Wiki Tier 1/2/3 validation below

## Living Wiki release validation

**This section must be completed for any release that touches:**

- `internal/livingwiki/`
- `internal/settings/livingwiki/`
- `internal/db/livingwiki_*`
- `web/src/app/(app)/repositories/[id]/page.tsx`
- `web/src/app/(app)/settings/living-wiki/page.tsx`
- `cmd/livingwiki-smoke/`

All eight steps below must be verified. Check each box and fill in the
observed result. Attach the activity-feed screenshot to the Release PR.

### Tier 1 — Unit-integration test (must pass before Release PR is merged)

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

3. DB migrations are forward-compatible. Rolling back the binary does not
   require reverting the migration — the new tables are ignored by the old binary.

### Release sign-off

Before merging the release-please PR:

- [ ] All Tier 1 tests pass (`make test-livingwiki-integration`)
- [ ] Tier 2 smoke passed (attach job log URL or Slack screenshot)
- [ ] All 8 Tier 3 steps verified with pass results noted above
- [ ] Rollback procedure tested (optional but recommended for major versions)

Reviewer: confirm all checkboxes are checked before approving the release PR.
