# Git credentials runbook

This page covers the runtime source-of-truth for git credentials
(default PAT and SSH key path) on a SourceBridge deployment after
**R3 slice 2** (`config-source-of-truth-r3`).

## TL;DR

- The DB-saved value (`ca_git_config.default_token`) is **always** the
  authoritative source when present.
- The env var (`SOURCEBRIDGE_GIT_DEFAULT_TOKEN`) is the **bootstrap-only
  fallback**: it is read on boot, captured by value into the resolver,
  and never wins over a saved DB value again.
- `default_token` is encrypted at rest under the `sbenc:v1` envelope
  (the same shape as `ca_llm_config.api_key`).

## Source order (per resolve)

For every clone / fetch / refresh / upstream probe:

1. **Workspace DB** (`ca_git_config`) — version-keyed cache, refetched
   when the version cell bumps. Save on replica A is visible on replica
   B's very next op.
2. **Env-bootstrap** (`cfg.Git`) — captured at boot from
   `SOURCEBRIDGE_GIT_DEFAULT_TOKEN` and `SOURCEBRIDGE_GIT_SSH_KEY_PATH`.
   Used only when the DB row is empty / unreachable.
3. **Builtin** — empty.

## Encryption-at-rest requirements

`default_token` is encrypted under the AES-GCM `sbenc:v1` envelope
when an encryption key is configured. Save behavior:

| `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` | `SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN` | Save of non-empty token |
|---|---|---|
| set | _any_ | OK — written under `sbenc:v1:` envelope |
| unset | unset / `false` | **422** — admin error: set the key |
| unset | `true` | OK — written as plaintext + 1 WARN per process |

Empty token saves are always accepted (no envelope needed).

## Fail-closed semantics (R3 slice 2)

When a workspace row exists but its `default_token` cannot be decrypted
(corrupt envelope, key rotation without re-save, missing
`SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY`):

- **Resolver returns `Snapshot.IntegrityError`**, empty Token, empty
  SSHKeyPath.
- **No silent fallback to env**: the env-only value is **not** returned.
- Live consumers (GraphQL clone/fetch/refresh, indexing service) abort
  with a clear "git credentials integrity failure" error rather than
  silently using stale or env-shadowed values.
- Admin UI surfaces the integrity error in the GET response so the
  operator sees what's wrong.

**Operator escape hatch**: a PUT that supplies a fresh non-empty
`default_token` is accepted as a re-key, replacing the corrupt row.
This is the supported recovery path; you do not need to reach into
SurrealDB manually.

## Multi-replica safety

Same shape as the LLM resolver:

- Every save bumps `ca_git_config.version`.
- Every Resolve probes the version cell first; the full row is fetched
  only on a version-stamp change.
- Result: an admin save on replica A is visible on replica B without
  restart, polling, or coordination.

## SSH key path validation

Server-side, on save:

- Empty allowed.
- Otherwise: absolute path, no `..`, no shell metacharacters
  (`; & | $ \` " ' ( ) < > * \` whitespace), no glob characters
  (`? [ ] { }`).
- Must reside under the configured allow-root (default
  `/etc/sourcebridge/git-keys/`; override via `SOURCEBRIDGE_GIT_SSH_KEY_PATH_ROOT`).
- Symlinks that resolve outside the allow-root are rejected.
- Non-existent paths under the root are accepted (lazy mount in
  Kubernetes is the common case).

## Migration command

```bash
# Re-encrypt any legacy plaintext default_token row under sbenc:v1.
# Idempotent — already-encrypted rows are skipped.
sourcebridge migrate-git-secrets
```

Requires `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` to be set (the command
refuses a no-op encryption).

## Verifying the fix in production

After deploy, tail the API pod logs and look for the
"git creds resolved" line on a real clone / refresh:

```bash
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=100 \
  | grep "git creds resolved"
```

Expected for a DB-saved token (with no env var set or with a different
env value):

```
git creds resolved op=clone token_set=true ssh_key_path_set=true
  version=N stale=false integrity_error_set=false
  sources_token=db sources_ssh_key_path=db
```

The `sources_token=db` is the smoking-gun confirmation that the legacy
"env wins" bug is closed.

## Rollback

If R3 slice 2 needs to be rolled back (regression in production), the
on-disk shape is forward-compatible: existing `sbenc:v1:` rows are
read transparently by the prior code path and the resolver's
env-bootstrap fallback returns the env value when the DB is
unavailable. Roll back the API binary; no data migration is required.

## See also

- [`llm-config.md`](llm-config.md) — parallel LLM credentials runbook.
- [`encryption-key-setup.md`](encryption-key-setup.md) — bootstrap the
  encryption key on a fresh deployment.
