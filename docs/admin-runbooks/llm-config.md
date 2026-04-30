# LLM credentials runbook

This page is the **operational checklist** for the LLM source-of-truth
program (R1 + R2 + R3 + followups). For the full architecture — resolution
order, encryption envelope, version-keyed cache shape, multi-replica
freshness — read [`docs/admin/llm-config.md`](../admin/llm-config.md). This
runbook deliberately does not duplicate that material so the two pages
cannot drift.

## TL;DR

- The workspace `/admin/llm` row is **always** the authoritative LLM
  configuration when present.
- `SOURCEBRIDGE_LLM_*` env vars are bootstrap fallbacks only; the saved
  DB value wins as soon as `/admin/llm` has been used at least once.
- `api_key` is encrypted at rest under the `sbenc:v1` envelope (the
  same shape as `ca_git_config.default_token`).
- An LLM-backed enqueue with an empty resolved provider is now
  **rejected** at the orchestrator (`ErrLLMProviderRequired`) — there is
  no silent attribution gap on the Monitor page anymore.

## Saving LLM settings

1. Open `https://<your-sourcebridge-host>/admin/llm`.
2. Set provider, base URL (optional for default-base providers like
   anthropic), API key, per-operation models, advanced mode (optional).
3. Save. The toast shows "LLM configuration saved." and the version
   banner bumps.

## Verifying the fix in production

After deploy, tail the API pod logs and look for the `llm config resolved`
line on a real LLM call:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=100 \
  | grep "llm config resolved"
```

Expected for a DB-saved configuration:

```
llm config resolved operation=knowledge repo_id=<id>
  provider=<your saved provider> model=<your saved model>
  base_url_set=<true|false> api_key_set=true ...
  sources_provider=workspace sources_api_key=workspace ...
```

The `sources_api_key=workspace` is the smoking-gun confirmation that
the legacy "env wins" bug is closed.

## Migration command

```bash
# Re-encrypt any legacy plaintext api_key row under sbenc:v1.
# Idempotent — already-encrypted rows are skipped.
kubectl -n sourcebridge exec deploy/sourcebridge-api -- \
  sourcebridge migrate-llm-secrets
```

Requires `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` to be set (the command
refuses a no-op encryption). For the encryption-key bootstrap procedure
on a fresh deployment, see
[`encryption-key-setup.md`](encryption-key-setup.md).

## Refusal behavior (R3 followups B1)

The orchestrator hard-blocks an LLM-backed enqueue whose `LLMProvider`
field resolved to empty:

```
llm provider required: LLM-backed enqueue resolved an empty provider:
  subsystem=knowledge job_type=cliff_notes;
  verify /admin/llm and the resolver wiring
```

When you see this in API logs:

1. Open `/admin/llm`. Confirm provider, API key, and (if needed) base
   URL are saved.
2. Tail `kubectl -n sourcebridge logs ... | grep llm_job_enqueue_missing_provider`
   — if it stops appearing after your save, the fix took effect.
3. If it persists, verify
   `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` is set on the API pod.
   Without it, the API refuses to read the encrypted `api_key` column
   and falls back to "" — which the orchestrator then refuses.

## Rollback

If R3 / R3-followups need to be rolled back (regression in production),
the on-disk shape is forward-compatible: existing `sbenc:v1:` envelope
rows are read transparently by the prior code path. Roll back the API
binary; no data migration required.

## Switching providers via profiles (April 30, 2026)

Profiles let you keep multiple LLM configurations side-by-side and
switch the active one with one click. Full architecture is in
[`docs/admin/llm-config.md`](../admin/llm-config.md#profiles); this
section is the operational checklist.

### Create a new profile

1. Open `https://<your-host>/admin/llm`.
2. With one or more profiles already saved, click **+ Add profile**
   (top-right when N=1, top-left of the list panel when N≥2). The
   editor lands on the new profile pre-named `Profile <N>`.
3. Set provider, base URL (optional for default-base providers), API
   key, models. Save.
4. The new profile appears in the list with `[radio]` unchecked — it
   exists but is not active.

### Switch the active profile

1. Click the radio button on the desired profile row.
2. The switch-confirmation modal lists what will change. Confirm.
3. The list updates (the new row gets the **Active** pill); the
   header pill updates; the resolver picks up the new active on the
   next worker call. No restart needed.

### Verify the switch end-to-end

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=100 \
  | grep "llm config resolved"
```

For a workspace-scoped resolve (no per-repo override), expect:

```
event="llm config resolved" operation=<op> repo_id=<R>
  provider=<active profile's provider> sources_provider=workspace
  active_profile_id=<id> ...
```

### Per-repo profile-mode override

When a single repo needs a different LLM than the workspace default
without rewriting the same fields:

1. Open Repositories → `<repo>` → wiki settings → "Advanced:
   per-repository LLM override".
2. Pick mode **Use a saved profile** and select from the dropdown.
3. Save. The override row is `{profile_id: "ca_llm_profile:<id>"}`.

End-to-end verification:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "llm config resolved" \
  | grep "repo_id=<your-repo-id>"
```

For a profile-mode override, expect:

```
event="llm config resolved" repo_id=<R>
  provider=<picked profile's provider>
  sources_provider=repo_override_profile ...
```

The label `repo_override_profile` distinguishes profile-mode from
inline-mode (`repo_override`). If you see `sources_provider=workspace`
unexpectedly, the override didn't take effect — verify the override
row in SurrealDB.

### Profile-missing detection

If the picked profile is deleted while a per-repo override still
references it, the GraphQL field resolver returns a non-fatal error
with extension code `PROFILE_NO_LONGER_EXISTS`. The repo's
wiki-settings panel renders a resolution panel with three actions:

- pick a different profile,
- switch to inline mode,
- revert to workspace inheritance.

Operator search string for the GraphQL response:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "PROFILE_NO_LONGER_EXISTS"
```

### Mutual exclusion at the row level

The per-repo override row holds AT MOST one of `profile_id` OR inline
fields, never both. The mutation enforces this; if database surgery
produces a dual-state row, the resolver treats inline as the winner
("inline takes precedence if both are somehow set"). If you see
`sources_provider=repo_override` on a repo you expected to be in
profile mode, query the row directly and verify which fields are set:

```bash
surreal sql --conn ... > SELECT id, living_wiki_llm_override
                          FROM lw_repo_settings WHERE repo_id="<R>";
```

## Boot-time profile migration

The first replica that starts up with the profile-aware code runs
`db.MigrateToProfiles` synchronously before the HTTP listener accepts
traffic. Each run emits a structured `slog.Info` line with a `source`
field (one of: `fresh-install-env-seed`, `legacy-empty`,
`legacy-ciphertext`, `legacy-plaintext-resealed`,
`legacy-plaintext-preserved`). See the
[migration source-label table](../admin/llm-config.md#boot-time-profile-migration-april-30-2026)
in the canonical doc for what each value means.

Quick search on a deployed cluster:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "llm profile migration"
```

If you see `source=legacy-plaintext-preserved` in production, the
encryption key is unset AND `SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true`
is in effect — investigate the configmap.

## Encryption-key rotation with profiles

See the [canonical doc](../admin/llm-config.md#encryption-key-rotation-with-profiles)
for the full procedure. One-page checklist:

1. Confirm replicas are healthy.
2. Roll the new `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` into the
   configmap (do NOT restart yet).
3. Re-save each profile via the admin UI (re-enter the API key + Save).
4. Re-save via the legacy `PUT /admin/llm-config` endpoint until the
   dual-read fallback is removed in a follow-up release.
5. `kubectl rollout restart deploy/sourcebridge-api`.
6. Verify with a worker call: `sources_api_key=workspace` in the
   `llm config resolved` log line.

## Two-phase update failure mode

`UpdateProfile` writes the row, then bumps the workspace version in a
separate batch. In the rare failure mode where the row update succeeds
but the version-bump batch fails, **peer replicas may serve stale
active config until the next workspace-touching write bumps the
version**.

Recovery is automatic: any subsequent profile write or activation
heals the gap. If the gap is observed at scale (frequent replica
divergence after profile updates), the fix is to refactor the cli
adapter (`cli/serve.go`) to use the BEGIN/COMMIT helpers directly so
the version bump is atomic with the row update.

Symptom search:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "version-bump batch failed"
```

## Rolling-deploy admin-write freeze (recommended for THIS rollout)

During the initial rolling deploy of the profile-aware version,
operators are RECOMMENDED to freeze admin writes on
`/admin/llm-config` and `/admin/llm-profiles` until every replica is
on the new image. The watermark scheme correctly handles writes that
leak through the freeze (the resolver detects and reconciles), but
the freeze eliminates the rare interleaving entirely. Belt-and-
suspenders.

Simplest freeze: scale the API to 1 replica for the rollout, OR add a
temporary 503 middleware in front of the two paths. Remove once
`kubectl get pods` shows every API pod on the new image.

## See also

- [`docs/admin/llm-config.md`](../admin/llm-config.md) — full architecture,
  resolution-order details, encryption envelope, multi-replica semantics,
  profile lifecycle, AST-lint contract.
- [`docs/admin-runbooks/git-config.md`](git-config.md) — parallel git
  credentials runbook.
- [`docs/admin-runbooks/encryption-key-setup.md`](encryption-key-setup.md)
  — bootstrap the encryption key on a fresh deployment.
