# LLM Configuration

This page documents how SourceBridge resolves which LLM provider, API key,
and model to use on every worker call — and how to verify that the
settings you saved in the admin UI are actually being applied.

The slug `workspace-llm-source-of-truth` in the codebase refers to the
April 2026 work that made the admin UI authoritative; before that, k8s
configmap env vars silently overrode database-saved settings, which
caused real production incidents (notably runaway Anthropic credit
consumption). The follow-up work `llm-provider-profiles` (April 2026)
generalized the single-row workspace config into a named-profile model
so operators can keep alternative configurations side-by-side and
switch between them without losing the previous values.

---

## Where to set things

| What | Where |
|---|---|
| LLM profiles (provider, API key, base URL, per-operation models, advanced mode) | **Admin → LLM** (`/admin/llm`) |
| Strategy / concurrency / refine-pass tuning for the comprehension engine | Admin → Comprehension (`/admin/comprehension`) |
| Model capability registry (context window, JSON mode, tool use) | Admin → Comprehension → Model Registry (`/admin/comprehension/models`) |
| Per-repository LLM override (advanced opt-in) | Repositories → `<repo>` → wiki settings → "Advanced: per-repository LLM override" |

If you landed on `/admin/comprehension/models` looking for "where do I
set the API key", you're in the wrong place. That page is the
**capability registry**, not the active configuration. The page itself
links to `/admin/llm` from a callout at the top.

---

## Profiles

A **profile** is a named bundle of LLM configuration: provider, API
key, base URL, per-operation models, timeout, and advanced-mode flag.
Every workspace has one or more profiles and exactly one **active**
profile at a time. The active profile is what every workspace-scoped
LLM operation uses, unless a per-repository override directs that
specific repository elsewhere.

The slug `llm-provider-profiles` in the codebase refers to the April
2026 work that introduced this model. The migration to profiles is
zero-downtime: the boot-time `MigrateToProfiles` step seeds a profile
named **Default** from the legacy `ca_llm_config:default` row's fields
(or from `SOURCEBRIDGE_LLM_*` env vars if the row is empty) on every
deployment of this version, then publishes a deterministic
`active_profile_id` pointer atomically.

### Storage

| Table / column | Role |
|---|---|
| `ca_llm_profile:<id>` | One row per profile. `name` is the displayed string; `name_key` is `lowercase(trim(name))` with a UNIQUE INDEX (case-insensitive uniqueness across replicas). `api_key` is encrypted at rest under the same `sbenc:v1` envelope as the workspace config (see Encryption). |
| `ca_llm_config:default.active_profile_id` | The pointer to the currently-active profile. Mutated atomically with `version` inside a CAS-guarded `BEGIN; ... COMMIT;` batch. |
| `ca_llm_config:default.version` | Monotonic version cell. Bumped by every profile create / update / delete / activation, every per-repo override save, every legacy `PUT /admin/llm-config`, and every reconciliation write. The resolver re-fetches the workspace overlay whenever it observes a version different from its cached snapshot. |
| `ca_llm_profile.last_legacy_version_consumed` | Watermark on the active profile only; advanced by every new-code write to the post-bump workspace.version. The resolver compares `workspace.version > active.last_legacy_version_consumed` to detect a write from an old (pre-profiles) pod and reconciles by writing the legacy row's contents through to the active profile. |

### Switching semantics

Activating a different profile bumps `workspace.version` atomically
with the `active_profile_id` pointer change. Every replica's
DefaultResolver picks up the new active profile on the very next
Resolve call (its tiny version-probe SELECT detects the bump). No
restart, no polling, no time-based TTL.

**Mid-flight jobs keep using the snapshot they were started with.** The
resolver freezes the snapshot for the duration of an in-flight LLM
operation (FrozenResolver pattern), so a switch between profiles does
not change the credentials of an already-running call. Jobs started
**after** the activation see the new active profile.

The `/admin/llm` switch confirmation modal warns about this explicitly
("Jobs already running keep using <from>. Jobs started after this
point use <to>."). The text is fixed by ruby UX intake §4.1 and
covered by tests; do not weaken without a paired UX update.

### Deletion semantics

The active profile **cannot** be deleted. The REST handler returns
`409` and the admin UI hides the Delete button on the active row.
Operators must switch to a different profile first.

A non-active profile that is referenced by a per-repo override **can**
be deleted; the override-bearing repository surfaces a "Profile no
longer exists" banner with three repair actions (pick another / switch
to inline override / revert to workspace inheritance). See "Per-repository
overrides" below.

Deleting a profile zeroes its `api_key` ciphertext via an
`UPDATE ... SET api_key = ''` followed by `DELETE FROM ca_llm_profile`.
This is defense-in-depth for backup snapshots — the tombstone holds no
ciphertext.

### Name uniqueness

Profile names are case-insensitive unique. The storage column
`name_key = lowercase(trim(name))` has a UNIQUE INDEX, so two replicas
attempting to create `Default` and `default` simultaneously will see
the loser receive a `409 ErrDuplicateProfileName`. The displayed
`name` preserves the user's casing and whitespace.

### Per-profile encryption invariant

Each profile's `api_key` is encrypted at rest **independently**, using
the same `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` and the same `sbenc:v1`
envelope as the legacy workspace config. The cipher object is built
**once** at boot in `cli/serve.go` and threaded into both the
`SurrealLLMConfigStore` and the `SurrealLLMProfileStore` via
`With...Cipher` options, so the two rows are guaranteed to be
encrypted under identical key material (librarian-M1 finding from the
plan review).

The lint test `TestNoDirectLLMProfileReads`
(`internal/llm/resolution/profile_lint_test.go`) prevents future code
from reaching around the architectural seams (`ProfileLookupStore` for
the resolver, `LLMProfileStoreAdapter` for REST handlers) and reading
profile rows directly. Allowlist entries and the `// llmprofile:allow`
on-line escape hatch document any deliberate exceptions.

---

## Resolution order

For every LLM-bearing worker RPC, the resolver picks the first non-empty
value across these layers, in order:

1. **Per-repository override (three modes)** — when the repo has a
   saved `LLMOverride` row, the override mode is one of:
   - **none** (default): the repo inherits the workspace layer below.
   - **profile**: `override.profile_id` references a saved profile.
     The resolver loads that profile and overlays its fields with
     source label `repo_override_profile`.
   - **inline**: the override carries inline provider/api_key/model
     fields. Source label `repo_override`.

   The mutation that saves the override enforces mutual exclusion at
   write time (one mode per row). The resolver also defensively treats
   a row with both `profile_id` set AND non-empty inline fields as
   inline mode (slice-3-flag #2: inline wins on collision). Empty
   fields fall through to the workspace layer.

2. **Workspace settings (the active profile)** — the resolver fetches
   the active profile via the version-keyed cache:
   - `workspace.version` is probed on every Resolve.
   - When the cached version is stale, the full active profile row is
     fetched, decrypted, and cached.
   - When `workspace.version > active.last_legacy_version_consumed`,
     the resolver reconciles the legacy `ca_llm_config:default` row's
     fields through to the active profile (rolling-deploy safety; old
     pods bump version on legacy `PUT /admin/llm-config` but do not
     advance the watermark, so the new resolver detects the gap).
   - When `active_profile_id` points at a missing profile, the
     resolver logs ERROR, returns an empty workspace overlay (env +
     builtin underneath), and latches `ErrActiveProfileMissing` so the
     admin UI shows the "Active profile is missing" repair banner.

   Source label `workspace`.

3. **Env-var bootstrap** — `cfg.LLM` populated at boot from
   `SOURCEBRIDGE_LLM_*` env vars (typically a k8s configmap). Only
   used when the workspace overlay leaves a field empty. Source label
   `env_fallback`.

4. **Built-in defaults** — `provider=anthropic`,
   `model=claude-sonnet-4-20250514`, `timeout_secs=900`. Non-empty
   defaults exist only for fields with sensible fallbacks; the API
   key defaults to empty and a call with no key fails fast at the
   worker. Source label `builtin`.

> **Naming note**: the SurrealDB column that stores the override is
> still `lw_repo_settings.living_wiki_llm_override` for backward
> compatibility. The Go type was renamed from `LivingWikiLLMOverride`
> to `LLMOverride` in slice 1 of plan
> `2026-04-29-workspace-llm-source-of-truth-r2.md` to reflect the
> widened scope. The legacy nested `model` JSON key is dual-written
> for one release cycle so a rollback to a pre-R2 binary still finds
> the model where it expects. The override row gained an additional
> `profile_id` field in slice 3 of `2026-04-29-llm-provider-profiles`
> for the profile-mode discriminator.

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
| `active_profile_id` | The currently-active profile's id at resolve time (empty when the dual-read legacy fallback path is in effect) |
| `sources_provider` | One of `repo_override_profile` / `repo_override` / `workspace` / `env_fallback` / `builtin` |
| `sources_api_key` | Same enum, indicating where the api_key came from |
| `sources_model` | Same enum |
| `sources_base_url` | Same enum |
| `sources_draft_model` | Same enum |
| `sources_timeout_secs` | Same enum |

**The verification ritual after a save**: hit the admin UI, save your
provider + API key, then trigger a small wiki regen. Every
`llm config resolved` line should show `sources_api_key=workspace` (or
`repo_override`/`repo_override_profile` if a per-repo override
applies). If you see `sources_api_key=env_fallback` after saving,
that's a bug — file an issue with the relevant log lines.

The activate handler additionally emits a `llm profile switched` line
on every activation so operators can correlate "why did the resolved
config change?" with admin actions:

```
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep "llm profile switched"
```

The line has `old_profile_id`, `new_profile_id`, `by` (the actor), and
`workspace_version` (the version stamp post-bump).

---

## Encryption at rest

Every workspace API key (the active profile's `api_key`, every other
profile's `api_key`, and the legacy `ca_llm_config:default.api_key`
mirror) is encrypted on disk with AES-256-GCM under a versioned
envelope: `sbenc:v1:` + base64(nonce || ciphertext). The encryption
key comes from `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` (32+ random
bytes, base64-encoded). Without that env var, the admin UI returns
422 when you try to save an API key (clear error message tells you
what to do).

For OSS development on a laptop where you don't want to deal with key
management, set `SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true`. The
save will land in plaintext with a one-time WARN in the log. **Never
use this in production.**

The cipher is constructed **once** at boot and threaded into both the
`SurrealLLMConfigStore` and the `SurrealLLMProfileStore`, so every
row encrypted by this version uses identical key material. There is
no per-profile key derivation; the security boundary is the workspace.

### Migrating legacy plaintext keys

Pre-April-2026 deployments saved API keys as plaintext. The new code
reads them transparently. The boot-time `MigrateToProfiles` step that
seeds the **Default** profile from the legacy row handles the three
api_key shapes explicitly (codex-H5):

| Legacy `api_key` shape | Migration behavior |
|---|---|
| `""` (empty) | Copy as-is. Empty stays empty. |
| `sbenc:v1:...` (already encrypted) | Copy bytes-for-bytes. No decrypt, no re-encrypt — preserves the existing nonce. |
| Plaintext (no `sbenc:` prefix) | Decrypt-passthrough then re-encrypt under the configured envelope. If `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` is unset AND `SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true`, the plaintext is preserved (OSS escape hatch). If neither is true, migration **HARD STOPS the boot** with `ErrEncryptionKeyRequired`. |

The migration emits a single structured `slog.Info` per run with a
`source` field naming exactly which path it took:

| `source` value | Meaning |
|---|---|
| `fresh-install-env-seed` | No prior `ca_llm_config:default` row. Seeded Default from `cfg.LLM` env vars (or empty values if env is unset). |
| `legacy-empty` | Legacy row existed but `api_key` was empty. Default profile created with empty key. |
| `legacy-ciphertext` | Legacy row's `api_key` started with `sbenc:v1:`. Copied bytes-for-bytes into Default. |
| `legacy-plaintext-resealed` | Legacy row's `api_key` was unprefixed plaintext. Decrypted via passthrough and re-encrypted under the current envelope. The legacy mirror is **not** updated by the migration; run the `migrate-llm-secrets` command to re-encrypt it post-deploy. |
| `legacy-plaintext-preserved` | Legacy row had unprefixed plaintext AND `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` was unset AND `SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true`. The plaintext was carried into Default unchanged. **Seeing this in a production-policy environment is a misconfiguration.** Investigate immediately. |

Operator log search:

```bash
# Confirm the migration ran on a replica.
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=500 \
  | grep "llm profile migration"

# Detect the misconfiguration case (plaintext preserved on a
# production policy).
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=500 \
  | grep "llm profile migration" \
  | grep "source=legacy-plaintext-preserved"
```

To re-encrypt any legacy plaintext rows that the migration did NOT
touch (the `ca_llm_config:default` mirror, when the migration took the
`legacy-plaintext-resealed` path):

```bash
kubectl -n sourcebridge exec -it deploy/sourcebridge-api -- sourcebridge migrate-llm-secrets
```

The command is idempotent — already-encrypted rows are re-encrypted
with a fresh nonce, which is fine. It refuses to run when
`SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` is unset.

### Encryption-key rotation with profiles

Rotating `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` after profiles are
deployed requires re-saving each profile's `api_key` so the
ciphertext is re-sealed under the new key. The procedure (xander-H1):

1. Generate the new 32-byte key:
   ```bash
   openssl rand -base64 32
   ```
2. Update `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` on the API and worker
   pods (both must agree). Restart the API pods.
3. Re-save **every** profile via `/admin/llm` (or the REST API). For
   each profile: open the editor, paste the api_key, click Save. The
   save path encrypts the supplied plaintext under the now-current
   key. Profiles you do not re-save remain encrypted under the OLD
   key and will fail to decrypt on the next Resolve, surfacing
   `ErrAPIKeyDecryptFailed` in the structured log.
4. Re-save the legacy mirror: hit `PUT /admin/llm-config` once with
   the current effective config (or hit Save on `/admin/llm` for the
   active profile, which dual-writes the legacy mirror via
   `writeActiveProfileWithLegacyMirror`). This is required only while
   the dual-read fallback window is open; once the post-rollout
   cleanup follow-up flips `dualReadFallbackEnabled = false` the
   legacy mirror is purely defensive.
5. Confirm via the verification log line:
   ```bash
   kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
     | grep "llm config resolved" | grep "api_key_set=true"
   ```
   `api_key_set=true` confirms decryption succeeded under the new
   key. `ErrAPIKeyDecryptFailed` (in any line) means a profile is
   still sealed under the old key — go back to step 3.

Schedule the dual-read-cleanup follow-up plan within one rollout
cycle of this delivery to close the window.

### Rolling-deploy admin-write freeze

During the rolling deploy of the version that introduces profiles
(when both old pre-profile pods and new profile-aware pods are
running concurrently), the recommendation is to **freeze admin
writes** on `/admin/llm-config` and `/admin/llm-profiles` until the
rollout completes (codex-H2).

The reason: old pods write only to the legacy `ca_llm_config:default`
columns and bump `workspace.version`, but they don't advance the
active profile's `last_legacy_version_consumed` watermark. A new pod
serving a Resolve after the gap detects the watermark drift and
reconciles legacy → active automatically. The reconciliation is
correct, idempotent, and CAS-guarded — but it does emit a
`llm legacy write reconciled` line per detected gap, which is noise
during a rolling deploy. Freezing admin writes avoids the noise and
keeps the per-rollout audit log clean.

After the rollout completes (no old pods left), the freeze can be
lifted; a subsequent admin write will not bump the watermark
incorrectly because every new-code write already advances it.

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

The block is collapsed by default. Inside, the form has a three-mode
radio (slice 3 of `2026-04-29-llm-provider-profiles`):

| Mode | Behavior |
|---|---|
| **None — inherit workspace** (default) | The repo uses the active profile via the workspace layer. No override row, no per-repo cost surface. |
| **Use a saved profile** | Picker dropdown of all workspace profiles (the active one is marked with the same Active pill as `/admin/llm`). The repo uses the picked profile's fields. The picker preview shows non-secret fields only — `api_key_hint` is **never** rendered (xander-L2). |
| **Use inline values** | The form mirrors `/admin/llm` exactly: provider dropdown, base URL, API key, advanced-mode per-area model fields. Inline values are stored and used directly; the api_key is encrypted at rest under the same envelope as workspace keys. |

The mutation enforces mutual exclusion at write time: setting `profileId`
and providing inline fields in the same request is rejected. Switching
modes in the UI does **not** destructively clear the previous mode's
values until Save is clicked, so toggling modes mid-edit is non-lossy.

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
| `profileId` non-empty | Override moves to **profile** mode; references the named profile. Inline fields ignored. |
| `clearProfile: true` | Drops the saved `profile_id`. Combined with non-empty inline fields = mode switch to **inline**. With nothing else = mode switch to **none**. |
| Field omitted (null) | Saved value preserved |
| Field is empty string `""` | That field cleared back to workspace inheritance |
| Field is non-empty (and `profileId` not in play) | That field set on the override (inline mode) |
| `apiKey` non-empty | Encrypts and replaces the saved cipher (inline mode only; profile mode uses the profile's key) |
| `apiKey` omitted/empty | Saved cipher preserved |
| `clearAPIKey: true` | Drops the saved cipher (revert to workspace key) |

The UI's "Clear saved API key" checkbox sets `clearAPIKey: true`. The
"Clear override" button calls `clearRepositoryLLMOverride` which drops
the entire override row.

### Profile-no-longer-exists handling (slice 3)

When a per-repo override references a profile that has since been
deleted, the GraphQL field resolver populates `profileName` as `null`
and attaches a non-fatal error with `extensions.code = "PROFILE_NO_LONGER_EXISTS"`.
The wiki-settings-panel detects this signal (slice-3-flag #3:
"saved `profileId` is non-empty AND `profileName` is null") and renders
an inline conflict-resolution panel inside the override section. The
panel offers three actions:

1. **Pick a different profile** — keeps profile mode, switches the
   reference.
2. **Switch to inline values** — converts the override to inline mode,
   pre-populating from the active profile's current values.
3. **Revert to workspace inheritance** — clears the override entirely.

For an operator debugging a "broken-override" report from a user, grep
the GraphQL response for `PROFILE_NO_LONGER_EXISTS`:

```bash
# Tail the API logs while the user reproduces.
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=200 \
  | grep -i "PROFILE_NO_LONGER_EXISTS\|profile_no_longer_exists"
```

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

The source labels distinguish the override mode end-to-end:

| `sources_provider=` | What's happening |
|---|---|
| `repo_override_profile` | Per-repo override in **profile** mode is taking effect. The provider/api_key/model values come from the named saved profile. |
| `repo_override` | Per-repo override in **inline** mode is taking effect. The values come from the inline fields on the override row. |
| `workspace` | No per-repo override is in effect (or the override is in **none** mode); the value comes from the active workspace profile. |
| `env_fallback` | The workspace overlay left the field empty; fell through to `cfg.LLM`. Usually a misconfiguration. |
| `builtin` | Both workspace and env left the field empty; fell through to the SourceBridge default. |

### Mutual-exclusion invariant

**The contract**: a per-repo override row is in exactly one of three
modes — none / saved-profile / inline. The GraphQL mutation enforces
this at write time:

- `setRepositoryLLMOverride(profileId: "<id>")` clears any inline
  fields on the row.
- `setRepositoryLLMOverride(<inline fields>, clearProfile: true)`
  drops `profile_id`.
- A request that sets both `profileId` and inline fields in the same
  call is rejected with a validation error.

So production rows should never carry both a non-empty `profile_id`
AND non-empty inline fields. The mutation is the source of truth for
mode discrimination.

**The defensive resolver behavior**: when the resolver encounters a
malformed row that DOES carry both (manual `surreal sql` repair, a
restored backup from a misbehaving older version, a hypothetical
future mutation bug), it picks **inline mode** —
`applyRepoOverride` checks for non-empty inline fields first and
falls into the inline branch when found, even if `profile_id` is
also set. The slice-3 instruction was explicit on this: "Mode-3
(inline) takes precedence if both are somehow set."

This is a deliberate, sanctioned deviation from the plan §117
narrative ("if `profile_id != ""`: fetch that profile"). The
reasoning:

1. **Locality of the credential**. Inline mode carries an
   encrypted api_key on the override row itself. If both are
   populated, the inline api_key was written deliberately by some
   recent caller; preferring profile mode would silently substitute
   a *different* key (the saved profile's). Inline-wins keeps the
   most-recently-written, most-specific, override credential.
2. **Defense-in-depth, not normal flow**. The dual-state row is
   already in an unsupported configuration. Either branch is
   defensible; the user's explicit choice records the intent.
3. **No silent credential drift**. The resolver emits source label
   `repo_override` (inline) rather than `repo_override_profile`,
   so an operator grepping the structured log immediately sees
   which mode was applied.

If an operator sees `sources_provider=repo_override` for a
repository they expected to be in profile mode, the override row is
in dual-state. Recovery: hit the override section in the
wiki-settings panel and Save once — the mutation re-asserts mutual
exclusion based on the current radio selection. The
`TestRepoOverrideProfile_DefensiveInlineWinsOnCollision` resolver
test pins this behavior so it can't drift silently.

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

## Diagnostics

### Active profile is missing

**Symptom**: the admin UI's `/admin/llm` page shows a red "Active
profile is missing" banner with a profile picker and an Activate
button. Editing is disabled until a profile is activated.

**What it means**: the `ca_llm_config:default.active_profile_id`
pointer is non-empty but does NOT match any row in `ca_llm_profile`.
Possible causes:

- An operator manually deleted profile rows via `surreal sql` without
  going through the admin UI's switch flow.
- The `ca_llm_profile` table was truncated (data restore from a
  pre-profiles backup, for example).
- The boot-time `MigrateToProfiles` failed mid-run on a previous
  version and was patched in this version, but no Resolve has yet
  observed the gap to surface the banner.

**Repair (UI)**: the banner's profile-picker lets the admin pick any
existing profile and activate it. If no profiles exist, the user can
create one via the `+ Add profile` action.

**Repair (CLI / SQL)**: the resolver's typed sentinel is
`resolution.ErrActiveProfileMissing`. To inspect the offending row
directly:

```bash
kubectl -n sourcebridge exec -it deploy/sourcebridge-api -- \
  sh -c 'surreal sql --conn ws://surrealdb:8000 --user root --pass "$SOURCEBRIDGE_DB_PASSWORD" --ns sourcebridge --db sourcebridge'
# At the surreal prompt:
#   SELECT id, active_profile_id, version FROM ca_llm_config:default;
#   SELECT id, name FROM ca_llm_profile;
```

If the active_profile_id points at an id that does not exist, either
manually update the pointer:

```surreal
UPDATE ca_llm_config:default SET active_profile_id = '<existing-profile-id>',
  version = version + 1, updated_at = time::now();
```

…or, for full-stack repair, invoke `POST /admin/llm-profiles/<id>/activate`
which goes through the CAS-guarded helper.

### Boot migration source-label log search

After every rollout, confirm the migration ran exactly once per
replica with the expected source path:

```bash
# All migration log lines from the last 1000 entries.
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=1000 \
  | grep "llm profile migration"

# Specific source paths:
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=1000 \
  | grep 'source=fresh-install-env-seed'        # never deployed before
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=1000 \
  | grep 'source=legacy-empty'                  # legacy row existed but had no api_key
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=1000 \
  | grep 'source=legacy-ciphertext'             # already-encrypted, copied bytes-for-bytes
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=1000 \
  | grep 'source=legacy-plaintext-resealed'     # plaintext re-encrypted under current key
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=1000 \
  | grep 'source=legacy-plaintext-preserved'    # MISCONFIGURATION in production policy
```

The first deploy of the profiles version on a brownfield install
should show one `legacy-*` line per replica. Subsequent deploys
should show no migration lines (the migration is idempotent and
fast-exits when the deterministic record id `ca_llm_profile:default-migrated`
already exists).

### Two-phase update: stale peer replicas

The `cli/llmProfileStoreAdapter.UpdateProfile` path is currently a
two-phase write (slice-1-flag #4): the row UPDATE is committed first,
then a separate CAS-guarded version-bump batch runs. The two phases
are CAS-guarded individually but not as a unit — there is a rare
failure mode where the row update commits but the version-bump batch
fails. The symptom: peer replicas continue serving the **old**
provider/api_key/model on cached snapshots until the next workspace
write bumps `version` and re-invalidates their caches.

**Recovery**: any subsequent profile create / update / activate will
heal the gap (it bumps the version cell, which invalidates peer
caches on the next Resolve probe). If the gap is observed at scale,
the recommended refactor is to fold the version bump into the
`UpdateProfile` BEGIN/COMMIT helper directly so the version bump is
atomic with the row update.

**Operator-facing log line for this case**: the row update emits the
usual `slog` write line; the version-bump failure emits
`llm profile update: workspace.version bump failed`. Grep:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=500 \
  | grep "workspace.version bump failed"
```

If you see this in steady state (not just transiently during a rolling
deploy), file an issue with the matching `id=` and the surrounding 5
lines for diagnosis.

### Architectural seam: AST lint

Every consumer of the named-profile storage MUST go through one of
two architectural seams:

- `resolution.ProfileLookupStore` — for the resolver-side per-repo
  override path.
- `rest.LLMProfileStoreAdapter` — for REST admin handlers.

The lint test `TestNoDirectLLMProfileReads` in
`internal/llm/resolution/profile_lint_test.go` walks every Go file
under `internal/`, `cli/`, and `cmd/` and fails CI on any new direct
`*db.SurrealLLMProfileStore` method call outside the documented
allowlist. The allowlist is small and each entry is justified in
context.

To allow a specific deliberate exception, write `// llmprofile:allow`
on or above the call. The escape hatch is reserved for receiver-name
ambiguity (e.g. an interface field that happens to share a name with
the concrete-store field convention); the resolver's
`r.profileStore.LoadProfileForResolution` call is currently the only
production use.

### Architectural seam: gqlgen regeneration quirk

When sibling resolver file signatures change, `gqlgen generate` may
absorb their implementations into the catch-all
`internal/api/graphql/schema.resolvers.go` instead of leaving them in
their dedicated sibling file. After a regen that touches the
profile-related resolvers (or any sibling), verify the dedicated file
still contains the implementations:

```bash
git diff internal/api/graphql/repository_llm_override.resolvers.go
git diff internal/api/graphql/schema.resolvers.go
```

If the dedicated file lost the impls, copy them back from
`schema.resolvers.go` and remove the duplicates from there before
committing. This is a known gqlgen behavior, not a bug in our wiring.

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
  attaches the resolved metadata. Two AST lint tests enforce no
  future bypass:
  - `internal/llm/resolution/lint_test.go` (`TestNoDirectWorkerLLMCallsOutsideLLMCall`)
    forbids direct `*worker.Client.<RPC>` calls outside the
    `llmcall.Caller` allowlist.
  - `internal/llm/resolution/profile_lint_test.go` (`TestNoDirectLLMProfileReads`)
    forbids direct `*db.SurrealLLMProfileStore` method calls outside
    the architectural-seam allowlist (`ProfileLookupStore` /
    `LLMProfileStoreAdapter`).

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

## Capability tiers and quality gates

Living Wiki generation evaluates each generated page against a quality
profile before accepting it. The quality gates are **tier-aware**: the
same gate that rejects low-density prose from a frontier model may be
relaxed to a warning (or have a looser threshold) for a local model that
cannot reliably meet frontier citation-density requirements.

### The three tiers

| Tier | `QualityGateTier` constant | Typical models |
|---|---|---|
| `frontier` | `modeltier.TierFrontier` | Claude, GPT-4-class, Gemini Ultra |
| `mid` | `modeltier.TierMid` | GPT-3.5-class, Gemini Flash, mid-size open-weights |
| `local` | `modeltier.TierLocal` | Ollama-served models, <30B open-weights (qwen3, llama3, phi4, etc.) |
| *(empty)* | `modeltier.TierUnknown` | Unclassified — falls back to pattern matching, then TierLocal |

`TierUnknown` is the zero value. Production code must never rely on
zero-value semantics for tier; the cold-start runner logs an ERROR when
it receives `TierUnknown` and the run falls through to the defensive
frontier profile (CA-150 D16: resolveErr → TierLocal).

### How to set the tier for a model

Go to **Admin → Comprehension → Model Registry** (`/admin/comprehension/models`).
Create or update an entry for the model and set `qualityGateTier` to
`frontier`, `mid`, or `local`. The registry key is the **model string
alone** (not `provider/model`) — if two providers use the same model
name, register them under distinct model IDs (e.g. `openrouter/anthropic/claude-3-5-sonnet`).

This is distinct from the active-model selection at **Admin → LLM**
(`/admin/llm`): the LLM page controls *which* model runs, while the
Model Registry controls *how strictly* the quality gates evaluate the
output.

### Pattern-match fallback

When a model is not in the registry the resolver calls
`modeltier.ClassifyByPattern(provider, model)`, which matches against a
built-in table of provider name prefixes and model name substrings (most
specific first). Operators whose provider is not in the built-in table
will land on `TierLocal` by default; to get a different tier, add an
explicit registry entry.

Operators can inspect which tier was resolved for a run by grepping the
structured log:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=500 \
  | grep "resolved quality-gate tier"
```

Each line includes `tier`, `source` (`registry` or `pattern`), and any
lookup error, alongside `provider` and `model`.

### Registry normalization

Model IDs are lower-cased and whitespace-trimmed before registry lookup,
so `"  Qwen3:32B  "` hits a `"qwen3:32b"` entry. Register models in
lowercase to rely on this normalization.

### Threshold table

The exact per-tier thresholds for every `(template, audience)` combination
live in `internal/quality/profile.go`. The `tierOverrides` map documents
which validators change between tiers and in which direction. Reading it
is the authoritative reference for operators tuning gate behaviour.
