# LLM Configuration

This page documents how SourceBridge resolves which LLM provider, API key,
and model to use on every worker call — and how to verify that the
settings you saved in the admin UI are actually being applied.

The slug `workspace-llm-source-of-truth` in the codebase refers to the
April 2026 work that made the admin UI authoritative; before that, k8s
configmap env vars silently overrode database-saved settings, which
caused real production incidents (notably runaway Anthropic credit
consumption). The follow-on `llm-provider-profiles` work (April 30, 2026)
added named provider profiles so a workspace can keep multiple
configurations (e.g. "Anthropic prod", "Local Ollama", "OpenAI eval")
and switch the active one with a single click.

---

## Where to set things

| What | Where |
|---|---|
| Profiles: create / activate / rename / duplicate / delete | **Admin → LLM** (`/admin/llm`) |
| Active profile fields (provider, API key, base URL, per-operation models, advanced mode) | **Admin → LLM** (the editor below the profile list) |
| Strategy / concurrency / refine-pass tuning for the comprehension engine | Admin → Comprehension (`/admin/comprehension`) |
| Model capability registry (context window, JSON mode, tool use) | Admin → Comprehension → Model Registry (`/admin/comprehension/models`) |
| Per-repository LLM override (advanced opt-in) | Repositories → `<repo>` → wiki settings → "Advanced: per-repository LLM override" |

If you landed on `/admin/comprehension/models` looking for "where do I
set the API key", you're in the wrong place. That page is the
**capability registry**, not the active configuration. The page itself
links to `/admin/llm` from a callout at the top.

---

## Resolution order

For every LLM-bearing worker RPC, the resolver picks the first non-empty
value across these layers, in order:

1. **Per-repository override** — when the repo has a saved
   `LLMOverride` row it applies to every repo-scoped LLM operation
   (summary, review, Q&A, knowledge, architecture diagrams, reports,
   living-wiki). Three modes are mutually exclusive at the row level:
   - **Workspace inheritance (no override)** — repo defers to workspace.
     `sources_provider=workspace` (etc.) on every resolve.
   - **Saved-profile mode** — `profile_id` references a row in
     `ca_llm_profile`. Resolver fetches the named profile and applies
     its fields. `sources_provider=repo_override_profile`.
   - **Inline mode** — repo holds its own provider / api_key / models
     in the override row itself. `sources_provider=repo_override`.

   Inline-mode and profile-mode overrides set the SAME area list as the
   workspace `/admin/llm` advanced-mode section; empty fields fall
   through to the workspace layer.

2. **Workspace settings** — the **active LLM profile**, surfaced via
   the legacy mirror row `ca_llm_config`. The active profile's id is
   stored in `ca_llm_config.active_profile_id`; the profile's fields
   are mirrored back into the legacy columns on every write so the
   resolver's existing read path keeps working unchanged.
   Read on every resolve via a version-keyed cache so an admin save (or
   activate-different-profile click) on replica A is visible to replica
   B on the very next call (no polling, no time-based TTL).
   `sources_provider=workspace`.

3. **Env-var bootstrap** — `cfg.LLM` populated at boot from
   `SOURCEBRIDGE_LLM_*` env vars (typically a k8s configmap).
   `sources_provider=env_fallback`.

4. **Built-in defaults** — `provider=anthropic`,
   `model=claude-sonnet-4-20250514`, `timeout_secs=900`. Non-empty
   defaults exist only for fields with sensible fallbacks; the API key
   defaults to empty and a call with no key fails fast at the worker.
   `sources_provider=builtin`.

### `sources_*` label cheat-sheet

The structured-log line `event="llm config resolved"` includes a
`sources_<field>` label per resolved field. The full enum:

| Label | Meaning |
|---|---|
| `repo_override` | Per-repo override in **inline mode** supplied this field. |
| `repo_override_profile` | Per-repo override in **saved-profile mode** supplied this field (the override pointed at a named profile and that profile's value won). |
| `workspace` | The active LLM profile (mirrored into `ca_llm_config`) supplied this field. |
| `env_fallback` | `SOURCEBRIDGE_LLM_*` env var supplied this field (no DB row had a non-empty value for it). |
| `builtin` | Hard-coded default supplied this field. |

> **Naming note**: the SurrealDB column that stores the override is
> still `lw_repo_settings.living_wiki_llm_override` for backward
> compatibility. The Go type was renamed from `LivingWikiLLMOverride`
> to `LLMOverride` in slice 1 of plan
> `2026-04-29-workspace-llm-source-of-truth-r2.md` to reflect the
> widened scope. The legacy nested `model` JSON key is dual-written for
> one release cycle so a rollback to a pre-R2 binary still finds the
> model where it expects.

The per-field `Sources` map on every snapshot tells you exactly which
layer supplied which value. Operators verify the fix landed by grepping
the structured log line:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "llm config resolved"
```

Every line includes:

| Field | Meaning |
|---|---|
| `operation` | The op constant (`discussion`, `living_wiki.coldstart`, `qa.synth`, etc.) |
| `repo_id` | The repository ID the call belongs to (empty for workspace-scoped calls) |
| `provider` | The resolved provider name (`anthropic`, `openai`, `ollama`, etc.) |
| `model` | The resolved model identifier |
| `api_key_set` | `true` when a non-empty key was resolved (the raw key is **never** logged) |
| `version` | The workspace DB version stamp at resolve time (zero when no DB row was used) |
| `stale` | `true` when the resolver served a cached snapshot because the DB was unreachable |
| `sources_provider` | One of `repo_override` / `workspace` / `env_fallback` / `builtin` |
| `sources_api_key` | Same enum, indicating where the api_key came from |
| `sources_model` | Same enum |
| `sources_base_url` | Same enum |
| `sources_draft_model` | Same enum |
| `sources_timeout_secs` | Same enum |

**The verification ritual after a save**: hit the admin UI, save your
provider + API key, then trigger a small wiki regen. Every
`llm config resolved` line should show `sources_api_key=workspace` (or
`repo_override` if a per-repo override applies). If you see
`sources_api_key=env_fallback` after saving, that's a bug — file an
issue with the relevant log lines.

---

## Encryption at rest

The workspace-saved API key is encrypted on disk with AES-256-GCM under
a versioned envelope: `sbenc:v1:` + base64(nonce || ciphertext). The
encryption key comes from `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` (32+
random bytes, base64-encoded). Without that env var, the admin UI
returns 422 when you try to save an API key (clear error message tells
you what to do).

For OSS development on a laptop where you don't want to deal with key
management, set `SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true`. The save
will land in plaintext with a one-time WARN in the log. **Never use
this in production.**

### Migrating legacy plaintext keys

Pre-April-2026 deployments saved API keys as plaintext. The new code
reads them transparently with a one-time migration WARN. To re-encrypt
under the v1 envelope:

```bash
kubectl -n sourcebridge exec -it deploy/sourcebridge-api -- sourcebridge migrate-llm-secrets
```

The command is idempotent — already-encrypted rows are re-encrypted
with a fresh nonce, which is fine. It refuses to run when
`SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` is unset.

### Boot-time profile migration (April 30, 2026)

When a SourceBridge replica starts up, it runs `db.MigrateToProfiles`
synchronously **before the HTTP listener accepts traffic**. The
migration reads the legacy `ca_llm_config:default` row and writes a
named profile `ca_llm_profile:default-migrated`, then sets
`ca_llm_config.active_profile_id` to point at it.

The migration is idempotent on the deterministic record id, so
concurrent boots converge on a single row without a UNIQUE-name retry
loop. Each run emits a structured `slog.Info` line with a `source`
field naming the path it took:

| `source` value | What it means | Operator action |
|---|---|---|
| `fresh-install-env-seed` | No legacy row existed; the migration seeded `Default` from `cfg.LLM` env vars (or built-in defaults if no env vars). | None — normal first boot. |
| `legacy-empty` | Legacy row existed but had no provider/api_key set; migration created an empty `Default` profile. | None — normal upgrade of an unconfigured deployment. |
| `legacy-ciphertext` | Legacy `api_key` was already encrypted under `sbenc:v1`; bytes copied verbatim into the profile row. | None — normal upgrade of a configured deployment. |
| `legacy-plaintext-resealed` | Legacy `api_key` was plaintext and the encryption key is configured; migration decrypted-then-re-encrypted into the profile row. | None — normal upgrade of a deployment that hadn't yet run `migrate-llm-secrets`. |
| `legacy-plaintext-preserved` | Legacy `api_key` was plaintext, encryption key NOT configured, AND the OSS escape hatch `SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true` is set. Plaintext was copied as-is. | **Investigate** — this is OSS-laptop-only behavior; seeing it in a production-policy environment is a misconfiguration. Set the encryption key and re-save the profile to seal under `sbenc:v1`. |

Search for the migration line after a rollout:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "llm profile migration"
```

If `source` is missing or the line never appears, the migration didn't
run cleanly and the boot likely failed (the API would have exited
non-zero — check the pod's restart count and previous-container logs).

---

## Per-repository overrides

Sometimes a single repository needs a different LLM than the workspace
default. Examples: a repository in a regulated environment that must
use a self-hosted Ollama for QA + knowledge generation; a repo
experimenting with a new model that the rest of the workspace isn't
ready to adopt.

Per-repository overrides land at:

> **Repositories → `<repo>` → Living Wiki settings → "Advanced:
> per-repository LLM override"**

The block is collapsed by default. Inside, the form mirrors `/admin/llm`
exactly: provider dropdown, base URL, API key, an "Advanced" toggle that
reveals per-area model fields (Code Review, Discussion & Q&A, Knowledge
Generation, Architecture Diagrams, Reports for enterprise, Draft Model
for local providers).

### What's overridable

The same fields the workspace `/admin/llm` advanced-mode section
exposes — there is no drift between the two surfaces. When a new
operation area is added to the workspace, it must be added to the
per-repo override at the same time so they stay aligned.

### Patch semantics

The `setRepositoryLLMOverride` GraphQL mutation behaves like a partial
update:

| Input | Effect |
|---|---|
| Field omitted (null) | Saved value preserved |
| Field is empty string `""` | That field cleared back to workspace inheritance |
| Field is non-empty | That field set on the override |
| `apiKey` non-empty | Encrypts and replaces the saved cipher |
| `apiKey` omitted/empty | Saved cipher preserved |
| `clearAPIKey: true` | Drops the saved cipher (revert to workspace key) |

The UI's "Clear saved API key" checkbox sets `clearAPIKey: true`. The
"Clear override" button calls `clearRepositoryLLMOverride` which drops
the entire override row.

### Verification

After saving a per-repo override, trigger an LLM operation on that repo
(any of: build understanding, regenerate wiki, ask a question via the
Q&A UI, run a code review). Tail the API logs and look for the
structured `llm config resolved` line:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "llm config resolved" \
  | grep "repo_id=<your-repo-id>"
```

For the override to be active, the source labels must show
`sources_provider=repo_override` (and similarly for `api_key`, `model`).

### Encryption

The override's API key is encrypted at rest under the same `sbenc:v1`
envelope as the workspace API key, using the same
`SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY`. Saves with a non-empty API key
fail with GraphQL extension code `ENCRYPTION_KEY_REQUIRED` when the env
var is unset (UI surfaces a clear actionable message).

### Storage caveat (legacy column name)

The override is stored in the `lw_repo_settings.living_wiki_llm_override`
SurrealDB column. The name dates back to the parent delivery when the
override was scoped to living-wiki ops only. The R2 widening kept the
column name to avoid a destructive migration; the Go type was renamed
from `LivingWikiLLMOverride` to `LLMOverride` to reflect the wider
scope. The legacy nested `model` JSON key is dual-written for one
release cycle to keep rollback safe.

---

## Why workspace beats env

This is the part that bit us in production. Pre-April-2026, the boot
path in `cli/serve.go` had inverted logic:

```go
// THE BUG — pre-April 2026:
if cfg.LLM.Provider == "anthropic" && rec.Provider != "" {
    // "Only override defaults — if env var was explicitly set, it takes priority"
    cfg.LLM.Provider = rec.Provider
}
if cfg.LLM.APIKey == "" && rec.APIKey != "" {
    cfg.LLM.APIKey = rec.APIKey
}
// ... etc — DB never wins when env was set.
```

When the configmap set `SOURCEBRIDGE_LLM_API_KEY`, that became the
hard floor. Saving via the admin UI updated the DB row, but the merge
at boot favored the (already-set) env var, so subsequent restarts
silently used the old configmap value. Living-wiki cold-start
additionally never attached `x-sb-llm-*` metadata at all, so it always
ran on the worker's bootstrap (configmap) provider regardless of UI
settings.

After April 2026:
- `cfg.LLM` is bootstrap-only, never mutated post-boot.
- The resolver reads workspace settings on every call via a version-
  keyed cache, so admin saves are visible immediately and cross-replica.
- Every LLM-bearing worker RPC flows through `llmcall.Caller` which
  attaches the resolved metadata. An AST lint test in
  `internal/llm/resolution/lint_test.go` enforces no future bypass.

For the full architectural rationale, see
`thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.md`.

---

## Multi-replica behavior

Every resolver instance does a tiny `SELECT version FROM ca_llm_config`
on every Resolve. If the version differs from the cached snapshot, it
fetches the full row, decrypts, and updates the cache. Cost is
sub-millisecond against a healthy SurrealDB.

A workspace save bumps the version atomically as part of the same
UPSERT. Replicas other than the one that handled the save will pick up
the new values on the *very next* worker LLM call — there's no polling
loop and no time-based TTL.

If the DB is unreachable when a Resolve fires, the resolver returns the
last known-good snapshot with `stale=true` and logs a WARN. This is
deliberately biased toward "stop the bleed" — better to keep using the
last workspace settings than to silently fall back to the configmap
defaults.

---

## Operator runbook: configmap cleanup

Once slices 1–3 are deployed and you've saved your settings via the
admin UI, the homelab configmap should stop being authoritative for the
LLM credentials. The full step-by-step is in
`thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth-
followups.md` (section A); the short version:

1. Save settings via `/admin/llm`.
2. `kubectl exec ... -- sourcebridge migrate-llm-secrets` to encrypt
   any legacy plaintext rows.
3. Verify with the log grep above (`sources_api_key=workspace`).
4. Edit the configmap to **remove** `SOURCEBRIDGE_LLM_API_KEY` and
   `SOURCEBRIDGE_WORKER_LLM_API_KEY`. **Keep**
   `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` (required) and
   `SOURCEBRIDGE_LLM_PROVIDER` as a no-op fallback.
5. Roll out via Argo. Re-verify with log grep.

The DB-saved settings persist across rollback; if step 5 misbehaves you
can re-add the removed env vars and you're back where you started.

---

## API↔worker gRPC mTLS (opt-in)

The API↔worker gRPC channel can run in two modes:

- **Default (insecure)** — `insecure.NewCredentials()`; the resolved API
  key crosses the hop in cleartext gRPC metadata. This is the OSS
  baseline and the dev-laptop path. Trust-zone assumption: API and
  worker run in the same pod or the same NetworkPolicy-isolated
  namespace. If your deployment violates that, you have a bigger
  problem than the LLM config.
- **Opt-in mTLS** — `SOURCEBRIDGE_WORKER_TLS_ENABLED=true` plus three
  paths to the cert/key/CA bundle. Both API and worker enforce
  client-cert auth. Slice 4 of plan
  `2026-04-29-workspace-llm-source-of-truth-r2.md` shipped the
  cert-manager Issuer + Certificate manifests at
  `deploy/kubernetes/mtls-*.yaml` plus the Go and Python wiring.

### Enabling mTLS in production

1. Confirm cert-manager is installed in the cluster.
2. Apply the cert-manager manifests (or include them in your overlay):
   - `mtls-issuers.yaml` — selfSigned + CA + ca-issuer
   - `mtls-worker-cert.yaml` — worker leaf
   - `mtls-api-cert.yaml` — API leaf (used as client identity)
3. Wait for the leaf Secrets to populate (~30s):
   ```bash
   kubectl -n sourcebridge get secret sourcebridge-worker-tls sourcebridge-api-tls
   ```
4. Patch the configmap to flip `SOURCEBRIDGE_WORKER_TLS_ENABLED` to
   `"true"` (the path env vars default to the standard mount points
   so you don't have to change them).
5. `kubectl -n sourcebridge rollout restart deploy/sourcebridge-api
   deploy/sourcebridge-worker`.
6. Tail the worker log for `worker_tls_enabled` and the API log for
   `worker tls enabled` to confirm.

### Cert rotation

cert-manager auto-renews the leaf cert 30 days before expiry (90-day
duration). The Secret updates atomically, but **the running gRPC
processes do not pick up the new cert until they restart**.

After cert-manager logs a renewal:

```bash
kubectl -n sourcebridge rollout restart deploy/sourcebridge-api deploy/sourcebridge-worker
```

This is a manual ~60-day cadence. Automated reload (Stakater Reloader
OR process-level hot-reload) is captured as a follow-up — see the new
followups doc.

### Failure modes

- TLS misconfig with `SOURCEBRIDGE_WORKER_TLS_ENABLED=true` → fail closed.
  The API refuses to start and the worker exits non-zero. There is no
  silent fallback to insecure once you've opted in.
- Mismatched ServerName → handshake fails at first dial; the API marks
  itself unready until the cert SAN matches.

---

## Profiles

A **profile** is a named, fully-formed LLM configuration: provider,
base URL, encrypted API key, per-operation models, advanced-mode flag,
timeout. Profiles live in the `ca_llm_profile` SurrealDB table, one row
per profile. The currently-active profile is pointed at by
`ca_llm_config.active_profile_id`; the resolver resolves through that
pointer on every call.

### Why profiles

The single-row `ca_llm_config` model worked when there was exactly one
useful provider configuration per workspace. As deployments adopt
multiple model vendors (Anthropic for review, Ollama for cold-start,
OpenAI for evaluation) the "edit fields then save then revert" workflow
becomes lossy and dangerous. Profiles make swapping a one-click
operation: each named configuration is a frozen snapshot, the editor
shows you exactly which one is live, and a switch is a CAS-guarded
write that bumps the workspace version atomically.

### The active-profile pointer

`ca_llm_config.active_profile_id` is the source of truth for "which
profile is live right now." Activating a profile is a single
BEGIN/COMMIT batch that:

1. Writes the new profile's fields into the legacy `ca_llm_config`
   columns (the legacy mirror — kept for the dual-read fallback window
   and for any rollback to a pre-profile binary).
2. Sets `ca_llm_config.active_profile_id` to the new profile's id.
3. Bumps `ca_llm_config.version` (the cache key every replica's
   resolver watches).
4. Advances `ca_llm_profile:<new>.last_legacy_version_consumed` so the
   reconciler knows its watermark is up to date.

The resolver picks up the new active profile on the very next call —
no polling, no time-based TTL.

### Operations

| Operation | Effect | Endpoint / path |
|---|---|---|
| Create | Add a new profile row. Name must be unique (case-insensitive). | `POST /api/v1/admin/llm-profiles` |
| Update fields | Patch any subset of fields (partial update; pointer-omitted = preserve). | `PUT /api/v1/admin/llm-profiles/<id>` |
| Rename | Same `PUT` with `{name}`; UI surfaces this as click-to-rename on the N=1 header pill (slice 4) or the [Edit] flow on the N≥2 list. | `PUT /api/v1/admin/llm-profiles/<id>` |
| Activate | Switch `active_profile_id` to point at this profile. | `POST /api/v1/admin/llm-profiles/<id>/activate` |
| Duplicate | Clone a profile (without the api_key — duplicates always start with `api_key_set=false`). | UI button → POST a new profile with the source's non-secret fields. |
| Delete | Drop a non-active profile. **The active profile cannot be deleted** — switch first. | `DELETE /api/v1/admin/llm-profiles/<id>` (returns 409 on the active profile). |

### Mutual exclusion at the row level

The per-repo override row (`lw_repo_settings.living_wiki_llm_override`)
holds AT MOST one of `profile_id` or inline fields, never both. The
GraphQL mutation enforces this at write time: setting `profileId`
clears any inline fields atomically; setting any inline field with a
non-empty value clears `profileId` atomically. Operators editing rows
directly via SurrealDB shell should preserve this invariant.

**Defensive collision behavior**: if surgery or a manual write somehow
produces a row with BOTH a non-empty `profile_id` AND inline fields,
the resolver treats inline as the winner ("inline takes precedence if
both are somehow set"). This matches the user's explicit slice-3
instruction — predictable behavior for an "impossible" state. If you
see `sources_provider=repo_override` on a repo you expected to be in
profile mode, query the row and check whether both fields are set:

```bash
surreal sql --conn ... --user ... --pass ...
> SELECT id, living_wiki_llm_override FROM lw_repo_settings WHERE repo_id = "<R>";
```

### Profile-missing detection signal in the UI

If a per-repo override references a `profile_id` that no longer exists
(deleted while another tab was editing), the API returns the override
data + a non-fatal GraphQL error with extension code
`PROFILE_NO_LONGER_EXISTS`. The admin UI's signal is **"saved
profileId is non-empty AND profileName is null"** — the wiki-settings
panel renders an inline resolution panel with three actions:

- Pick a different profile (re-point the override).
- Switch the override to inline mode (re-enter fields manually).
- Revert to workspace inheritance (clear the override).

Operator search string when debugging a "broken-override" report:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "PROFILE_NO_LONGER_EXISTS"
```

---

## Encryption-key rotation with profiles

The slice-1-flag #4 risk surfaced during the slice-1 ship-debrief:
`UpdateProfile` is implemented as a two-phase write — the row update
goes through one CAS-guarded batch, then a separate batch advances
the workspace version. In the rare failure mode where the row update
succeeds but the version-bump batch fails, **peer replicas may serve
stale active config until the next workspace-touching write bumps the
version**. The recovery is automatic: any subsequent profile write or
activation heals the gap. If the gap is observed at scale, refactor
the adapter to use the `BEGIN/COMMIT` helpers directly so the version
bump is atomic with the row update.

To rotate the encryption key now that profiles exist:

1. **Confirm replicas are healthy.** `kubectl get pods` — every
   sourcebridge-api pod should be Ready.
2. **Roll the new key.** Update `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY`
   in the configmap. **Don't** restart yet.
3. **Re-save each profile.** For every profile, open `/admin/llm`,
   pick the profile via [Edit] on the N≥2 list, re-enter the API key,
   and Save. This re-encrypts under the new key. Watch for
   `event="llm profile updated" profile_id=<id> api_key_set=true` in
   the logs to confirm.
4. **Re-save via the legacy endpoint** (until the dual-read fallback
   is removed in a follow-up release). `PUT /api/v1/admin/llm-config`
   with the active profile's fields keeps the legacy mirror current.
5. **Restart the deployment.** `kubectl rollout restart
   deploy/sourcebridge-api`. The new pods read the rotated key from
   the configmap on boot.
6. **Verify with a worker call.** Trigger a small wiki regen and look
   for `event="llm config resolved" sources_api_key=workspace`. If
   you see `ErrAPIKeyDecryptFailed` warnings, a profile didn't get
   re-saved — return to step 3.

Schedule the dual-read-cleanup follow-up plan within one rollout cycle
of the rotation so the legacy `ca_llm_config:default` ciphertext is
no longer load-bearing.

---

## Rolling-deploy admin-write freeze (recommended)

During the rolling deploy of THIS version (the one that introduces
profiles), operators are RECOMMENDED to freeze admin writes on
`/admin/llm-config` and `/admin/llm-profiles` until the rollout is
complete. This is a belt-and-suspenders complement to the watermark
scheme: it eliminates the "old pod accepts a write between two new-pod
resolves" interleaving entirely.

The watermark scheme correctly handles writes that DO leak through the
freeze — the resolver detects a stale legacy version, reconciles legacy
→ active profile via a CAS-guarded write-through, and re-anchors the
watermark. The freeze is a precaution, not a correctness requirement.

The simplest freeze: scale the API deployment to a single replica for
the duration of the rollout, OR add a temporary `503` middleware in
front of the two paths via Traefik. Document the freeze in your
deploy runbook and remove it once `kubectl get pods` shows every
sourcebridge-api pod on the new image.

---

## Diagnostics: active profile is missing

`ErrActiveProfileMissing` fires when the resolver looks up
`ca_llm_config.active_profile_id` and finds it points at a profile that
no longer exists. Possible causes:

- **Operator surgery**: someone deleted the profile row directly via
  `surreal sql` instead of going through the API.
- **Failed migration** on a fresh cluster where the boot path wrote
  the pointer but didn't write the profile (extremely narrow window —
  the migration is a single BEGIN/COMMIT, so this requires the
  SurrealDB write to partially succeed).
- **Bulk delete via the API path**: not possible — the API rejects
  deletion of the active profile.

The admin UI at `/admin/llm` detects the gap from the LIST response's
`active_profile_missing: true` flag and renders a red **repair
banner** at the top of the page:

> **Active profile is missing**
> The workspace has profiles, but the active-profile pointer no longer
> matches any of them. Pick a profile to activate. Editing is
> disabled until you do.

The repair UX is a single picker + Activate button. After activation,
the page reloads and the banner clears.

If the repair UI itself is unreachable (API unavailable, no profiles
left), the recovery is direct DB inspection:

```bash
kubectl -n sourcebridge exec -it deploy/sourcebridge-api -- \
  surreal sql --conn ws://sourcebridge-surreal:8000 ...

> SELECT id, name FROM ca_llm_profile;
> SELECT active_profile_id FROM ca_llm_config;
```

If `ca_llm_profile` is empty, restart the deployment — the boot
migration will re-seed `Default` from the legacy `ca_llm_config` bytes
(or from `cfg.LLM` env vars on a truly fresh cluster).

---

## gqlgen regeneration quirk

When a sibling `*.resolvers.go` file's resolver method signatures
change (slice 3 added the `RepositoryLLMOverride.profileName` field
resolver, which prompted gqlgen to regenerate the schema), gqlgen may
absorb the new resolver implementations into `schema.resolvers.go`
alongside the original siblings. Always verify after a `make proto`
or `go generate ./internal/api/graphql/...`:

```bash
# The sibling file should still own its resolvers — they should NOT
# also appear in schema.resolvers.go.
grep -n "func .* RepositoryLLMOverride.*ProfileName" \
  internal/api/graphql/repository_llm_override.resolvers.go \
  internal/api/graphql/schema.resolvers.go
```

If the resolver appears in BOTH files, the build will fail on duplicate
method declaration. Move the duplicate out of `schema.resolvers.go`
back into the sibling file before committing.

---

## Adding a new LLM-bearing worker RPC

The contract is enforced in three places:

1. **`internal/llm/resolution/ops.go`**: define a new op constant and
   add it to `KnownOps`. The resolver rejects unknown ops at test
   time so a typo can't silently route through the wrong defaults.
2. **`internal/worker/llmcall/llmcall.go`**: add the method to the
   `WorkerLLM` interface and add a wrapper on `*Caller`.
3. **`internal/llm/resolution/lint_test.go`**: add the method name to
   `protectedWorkerMethods`. The AST lint test fails if any caller
   outside the `llmcall.Caller` allowlist invokes the method directly.

The compile-time assertion `var _ WorkerLLM = (*worker.Client)(nil)` in
`internal/worker/llmcall/interface_test.go` catches the third leg —
adding to the interface without implementing it on `*worker.Client`
breaks the build immediately.

---

## Adding a new direct-DB read of a profile

Direct production-code reads of `ca_llm_profile` (whether by table
name, by `expr["api_key"]` map access, or by a `.LLMProfile.<field>`
selector chain) are forbidden by the AST lint
`internal/llm/resolution/profile_lint_test.go::TestNoDirectLLMProfileReads`.
The lint runs in ENFORCE mode from day one; CI fails on a regression.

The intended consumption pattern is:

1. The store layer (`internal/db/llm_profile_store.go`) exposes typed
   accessors that strip credentials at the boundary.
2. The resolver uses the narrow `ProfileAwareProfileStore` interface
   (`internal/llm/resolution/profile_aware_adapter.go`).
3. The GraphQL layer uses the narrow `LLMProfileLookup` interface
   (`internal/api/graphql/resolver.go`).
4. The REST layer uses the narrow `LLMProfileStoreAdapter` interface
   (`internal/api/rest/llm_profiles.go`).

If a new use case truly cannot fit any of those interfaces, add the
file to the allowlist in `profile_lint_test.go::profileLintAllowlist`
AND add a `// LLMPROFILE_LINT_OK: <reason>` comment in the touched
file. The lint self-test (`TestProfileLintSelfTest`) verifies the
three patterns are caught on synthetic violations so the lint can't
silently regress.
