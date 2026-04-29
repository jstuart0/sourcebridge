# Encryption-key setup runbook

This page documents the manual operator procedure for bootstrapping
`SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` on a fresh SourceBridge
deployment. The encryption key is required for the R1+R2+R3
source-of-truth program: it encrypts every secret-bearing DB column
(`ca_llm_config.api_key`, `ca_git_config.default_token`,
per-repo `living_wiki_llm_override.api_key_cipher`) at rest under the
`sbenc:v1` AES-GCM envelope.

This procedure is **out of band** from the GitOps pipeline because:

- The key itself is a secret — it cannot live in a git-tracked
  manifest.
- The k8s secret update is a live cluster mutation that should not run
  inside an automated orchestration pass.
- The migrate commands are one-shot operator actions that only make
  sense after the key is live.

The Mozart orchestration pauses at this step on every deployment that
would touch the encryption key; the operator runs the steps below
manually.

## When you need to run this

- **Fresh homelab/thor deployment** before saving any LLM/git
  credentials via `/admin/llm` or `/admin/git`.
- **Rotating the key** (rare; only on a confirmed compromise — the
  rotation invalidates every existing `sbenc:v1:` ciphertext and
  requires every credential to be re-saved through the admin UI).

## Steps

### 1. Generate a 32+ character passphrase

```bash
openssl rand -base64 32
```

Copy the output. Don't paste it into a chat client or email.

### 2. Store it in Vaultwarden

- Open https://vault.xmojo.net.
- Search for the entry "SourceBridge".
- Add (or update) the field
  `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` with the value from step 1.
- Save the entry. Vaultwarden is the source of truth from this point
  on; if the cluster ever loses the secret, the operator restores it
  from here.

### 3. Add it to the `sourcebridge-secrets` k8s secret

The secret is in the `sourcebridge` namespace and is **excluded from
Argo sync** (the ArgoCD Application's `ignoreDifferences` block).
Manual `kubectl edit` is the supported flow.

```bash
# Verify context.
kubectl config current-context  # expected: thor or kubernetes-admin@kubernetes

# Edit the secret.
kubectl -n sourcebridge edit secret sourcebridge-secrets
```

In the editor, add a new `data:` entry. Values in `data:` are
base64-encoded; encode the passphrase first:

```bash
echo -n '<passphrase from step 1>' | base64
```

Add the encoded value:

```yaml
data:
  SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY: <base64-encoded value>
  # ... existing entries unchanged ...
```

Save and exit. `kubectl` confirms with
`secret/sourcebridge-secrets edited`.

### 4. Restart the API and worker pods so they re-read the secret

```bash
kubectl -n sourcebridge rollout restart deployment/sourcebridge-api
kubectl -n sourcebridge rollout restart deployment/sourcebridge-worker
kubectl -n sourcebridge rollout status deployment/sourcebridge-api --timeout=120s
kubectl -n sourcebridge rollout status deployment/sourcebridge-worker --timeout=120s
```

The Stakater Reloader (when installed — see homelab repo
`manifests/reloader/`) automates this restart on every secret change;
without Reloader, the manual rollout is required.

### 5. Verify the key is live

```bash
# The pods should log the encryption-key presence on boot.
kubectl -n sourcebridge logs -l app=sourcebridge-api --tail=50 \
  | grep -i "encryption"
```

Expected: a structured log line mentioning the encryption-key being
configured (or, on a fresh empty DB, no errors). You should NOT see
errors about "encryption key not set."

### 6. Run the migration commands

These re-encrypt any pre-existing plaintext rows under the new
`sbenc:v1` envelope. Idempotent — already-encrypted rows are skipped.

```bash
kubectl -n sourcebridge exec deploy/sourcebridge-api -- \
  sourcebridge migrate-llm-secrets

kubectl -n sourcebridge exec deploy/sourcebridge-api -- \
  sourcebridge migrate-git-secrets
```

Expected output: `... re-encrypted under sbenc:v1 envelope` (or a
note that there's nothing to migrate, on a fresh DB).

### 7. Verification grep

After saving any credential through `/admin/llm` or `/admin/git`,
confirm the `sbenc:v1:` prefix is present in the saved column:

```bash
# Direct SurrealDB query through the API pod.
kubectl -n sourcebridge exec deploy/sourcebridge-api -- \
  sh -c 'curl -fsSL -X POST \
    -H "Accept: application/json" \
    -H "NS: $SOURCEBRIDGE_STORAGE_SURREAL_NAMESPACE" \
    -H "DB: $SOURCEBRIDGE_STORAGE_SURREAL_DATABASE" \
    --user "$SOURCEBRIDGE_STORAGE_SURREAL_USER:$SOURCEBRIDGE_STORAGE_SURREAL_PASS" \
    --data "SELECT string::starts_with(api_key, \"sbenc:v1:\") AS encrypted FROM ca_llm_config;" \
    "${SOURCEBRIDGE_STORAGE_SURREAL_URL/ws:/http:}/sql"'
```

Expected: `[{"encrypted": true}]` (when a key has been saved).

## Failure modes and recovery

- **`migrate-llm-secrets refuses to run`**: the command refuses a
  no-op encryption (key not set). Confirm `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY`
  is in the secret, then restart the pod, then re-run.
- **Save through `/admin/llm` returns 422 with "encryption key
  required"**: the API refused to write a plaintext API key without
  the encryption key. Same recovery: confirm the secret + restart.
- **Existing rows can't be decrypted after a key change**: you rotated
  the key without first re-saving the secrets. Restore the previous
  key from Vaultwarden, OR re-save every secret through the admin UI
  under the new key.

## See also

- [`docs/admin/llm-config.md`](../admin/llm-config.md) — encryption
  envelope details and resolution architecture.
- [`docs/admin-runbooks/llm-config.md`](llm-config.md) — LLM-config
  operational checklist.
- [`docs/admin-runbooks/git-config.md`](git-config.md) — git-config
  operational checklist.
