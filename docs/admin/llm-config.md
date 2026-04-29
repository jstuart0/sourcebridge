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
| Per-repo living-wiki LLM override (advanced opt-in) | (UI lands alongside GraphQL surface — followup B.6) |

If you landed on `/admin/comprehension/models` looking for "where do I
set the API key", you're in the wrong place. That page is the
**capability registry**, not the active configuration. The page itself
links to `/admin/llm` from a callout at the top.

---

## Resolution order

For every LLM-bearing worker RPC, the resolver picks the first non-empty
value across these layers, in order:

1. **Per-repo override** — only when the operation is in the
   `living_wiki.*` family AND the repo has a saved
   `LivingWikiLLMOverride` row. Other operations (QA, comprehension,
   reports) ignore the override even when it's set.
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

## Trust-zone constraint (and the mTLS follow-up)

The API↔worker gRPC channel uses `insecure.NewCredentials()` today —
the resolved API key crosses the hop in cleartext gRPC metadata. This
is a documented trust-zone assumption: API and worker should run in the
same pod or the same NetworkPolicy-isolated namespace. If your
deployment violates that, you have a bigger problem than the LLM
config.

mTLS on this channel is captured as a follow-up (followups doc section
B.1). It requires cert generation/rotation, gRPC server-side TLS in
the Python worker, and matching credentials in the Go client.

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
