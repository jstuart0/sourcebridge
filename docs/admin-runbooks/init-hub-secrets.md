# init-hub-secrets runbook

`scripts/init-hub-secrets.sh` generates a `.env` file with strong random
secrets for the `docker-compose.hub.yml` self-hosted distribution.

## When to run

Run once before starting the stack for the first time. The default compose
file ships sentinel values (`INSECURE-DEFAULT-CHANGE-ME-NOW`) that the API
server detects at boot and logs as a repeating warning until replaced.

Also run with `--force` to rotate secrets (e.g. after a suspected
credential leak, or as part of periodic rotation policy).

## What it produces

`scripts/init-hub-secrets.sh` writes `.env` in the current directory with
three variables:

| Variable | Purpose |
|---|---|
| `SURREAL_PASS` | SurrealDB admin password |
| `SOURCEBRIDGE_GRPC_SECRET` | Shared secret between the API server and the AI worker |
| `SOURCEBRIDGE_JWT_SECRET` | JWT signing key for all user sessions |

Each value is 64 hex characters (256 bits) generated via `openssl rand -hex 32`.
The file is written `chmod 0600`.

## How to run

```bash
# Initial setup
curl -O https://raw.githubusercontent.com/sourcebridge-ai/sourcebridge/main/scripts/init-hub-secrets.sh
chmod +x init-hub-secrets.sh
./init-hub-secrets.sh

# Then start the stack — picks up .env automatically
docker compose -f docker-compose.hub.yml up -d
```

```bash
# Rotate secrets (invalidates all active sessions)
./init-hub-secrets.sh --force
docker compose -f docker-compose.hub.yml restart
```

## What if it fails

**`openssl not found`**: install OpenSSL.
- macOS: `brew install openssl`
- Debian/Ubuntu: `apt-get install openssl`
- RHEL/Fedora: `dnf install openssl`

**`.env already exists` (exit 1 without `--force`)**: this is a guard to
prevent accidental overwrite. If you want to regenerate, pass `--force`.
Note that rotating secrets invalidates all active user sessions.

**Output path override**: set `SECRETS_OUTPUT_FILE` to redirect output:
```bash
SECRETS_OUTPUT_FILE=/etc/sourcebridge/.env ./init-hub-secrets.sh
```

## Keeping .env out of git

`.env` is listed in `.gitignore`. Do not commit it. If it is accidentally
committed, rotate immediately with `--force`, push a new commit that removes
the file, and consider the old secrets compromised.

## Verification

After running the script, the API server should start without the security
warning:

```
WARN  insecure default credentials detected — set SURREAL_PASS, SOURCEBRIDGE_GRPC_SECRET, SOURCEBRIDGE_JWT_SECRET before exposing to a network
```

If the warning still appears after restart, confirm the `.env` file is in
the directory where you run `docker compose`, and that `docker compose -f docker-compose.hub.yml config` shows non-sentinel values for the three variables.

## Related

- README "Security" callout: [`README.md`](../../README.md)
- Source: [`scripts/init-hub-secrets.sh`](../../scripts/init-hub-secrets.sh)
- Plane ticket: [CA-155](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/797d0038-6493-49dc-8307-d7c54d3f6611/) (Phase 5 Slice 1)

---
*Documented by scott on 2026-05-04.*
