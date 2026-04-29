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

## See also

- [`docs/admin/llm-config.md`](../admin/llm-config.md) — full architecture,
  resolution-order details, encryption envelope, multi-replica semantics.
- [`docs/admin-runbooks/git-config.md`](git-config.md) — parallel git
  credentials runbook.
- [`docs/admin-runbooks/encryption-key-setup.md`](encryption-key-setup.md)
  — bootstrap the encryption key on a fresh deployment.
