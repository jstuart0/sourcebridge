# LLM Configuration

This page documents how SourceBridge resolves which LLM provider, API key,
and model to use on every worker call — and how to verify that the
settings you saved in the admin UI are actually being applied.

The slug `workspace-llm-source-of-truth` in the codebase refers to the
April 2026 work that made the admin UI authoritative; before that, k8s
configmap env vars silently overrode database-saved settings, which
caused real production incidents (notably runaway Anthropic credit
consumption).

---

## Where to set things

| What | Where |
|---|---|
| Provider, API key, base URL, per-operation models, advanced mode | **Admin → LLM** (`/admin/llm`) |
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
   `LLMOverride` row, those fields apply to every repo-scoped LLM
   operation for that repo (summary, review, Q&A, knowledge,
   architecture diagrams, reports, living-wiki). Mirrors the workspace
   `/admin/llm` advanced-mode area list exactly. Empty fields fall
   through to the workspace layer.
2. **Workspace settings** — the saved `ca_llm_config` row, populated by
   the admin UI. Read on every resolve via a version-keyed cache so an
   admin save on replica A is visible to replica B on the very next
   call (no polling, no time-based TTL).
3. **Env-var bootstrap** — `cfg.LLM` populated at boot from
   `SOURCEBRIDGE_LLM_*` env vars (typically a k8s configmap).
4. **Built-in defaults** — `provider=anthropic`,
   `model=claude-sonnet-4-20250514`, `timeout_secs=900`. Non-empty
   defaults exist only for fields with sensible fallbacks; the API key
   defaults to empty and a call with no key fails fast at the worker.

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
