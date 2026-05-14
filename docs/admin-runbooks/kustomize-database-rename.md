# Kustomize DATABASE rename — `main` → `sourcebridge`

**Applies to**: Kustomize operators with an existing SourceBridge install that has data in
SurrealDB under the legacy database name `main`. This runbook describes how to migrate that
data to the canonical `sourcebridge` database name introduced in CA-345.

**Skip this runbook if any of the following are true**:
- You are doing a fresh kustomize install (no existing data).
- You deploy via Helm (the Helm chart already uses `sourcebridge`; it is unchanged).
- Section 1 (Detection) reports a count of 0.

If you skip the runbook, simply apply the updated configmap. The API and worker will connect
to the (now-empty) `sourcebridge` database and behave as a fresh install. If you have data
you want to preserve, complete this runbook first.

---

## Section 1 — Detection

Run the following to check whether the legacy `main` database holds any data. If the count
is 0 (or the database does not exist), **skip the rest of this runbook** — apply the Phase 8
configmap change directly.

```bash
kubectl -n sourcebridge exec deploy/sourcebridge-surrealdb -- \
  surreal sql --conn http://localhost:8000 \
             --user "$SURREAL_USER" --pass "$SURREAL_PASS" \
             --ns sourcebridge --db main \
             -q "SELECT count() FROM ca_repository GROUP ALL"
# If the returned count is 0 (or the database does not exist),
# SKIP this runbook — apply the Phase 8 configmap change directly.
# If the count is non-zero, continue with sections 2-6.
```

---

## Section 2 — Scale API and worker to 0 replicas

Ensures no concurrent writers are active during the export. Do not proceed to Section 3
until all pods have terminated.

```bash
kubectl -n sourcebridge scale deploy sourcebridge-api --replicas=0
kubectl -n sourcebridge scale deploy sourcebridge-worker --replicas=0
# Wait for pods to terminate before continuing.
kubectl -n sourcebridge wait --for=delete pod -l app.kubernetes.io/component=api --timeout=120s
kubectl -n sourcebridge wait --for=delete pod -l app.kubernetes.io/component=worker --timeout=120s
```

---

## Section 3 — Export from `main`

Use SurrealDB's official `surreal export` CLI to dump the `main` database to a file inside
the SurrealDB pod. The file lives at `/tmp/main-export.surql` in the pod's filesystem.

```bash
kubectl -n sourcebridge exec deploy/sourcebridge-surrealdb -- \
  surreal export --conn http://localhost:8000 \
                --user "$SURREAL_USER" --pass "$SURREAL_PASS" \
                --ns sourcebridge --db main \
                /tmp/main-export.surql
```

---

## Section 4 — Verify export is non-empty

This is a safety net. If the file is empty or missing, the export failed and you should
**STOP** here. Do not proceed with the import; investigate the export failure (check
SurrealDB pod logs: `kubectl -n sourcebridge logs deploy/sourcebridge-surrealdb`).

```bash
kubectl -n sourcebridge exec deploy/sourcebridge-surrealdb -- \
  sh -c 'wc -l /tmp/main-export.surql'
# Expected output: a non-zero line count. If 0 or the file is missing,
# STOP. Do not proceed with the import; investigate the export failure.
```

---

## Section 5 — Import into `sourcebridge`

Import the export file into the `sourcebridge` database. SurrealDB creates the database
automatically on first write if it does not already exist.

**Note**: SurrealDB does not expose a `db rename` statement. The export + import approach
is the official migration path.

```bash
kubectl -n sourcebridge exec deploy/sourcebridge-surrealdb -- \
  surreal import --conn http://localhost:8000 \
                --user "$SURREAL_USER" --pass "$SURREAL_PASS" \
                --ns sourcebridge --db sourcebridge \
                /tmp/main-export.surql
```

---

## Section 6 — Apply Phase 8 configmap change and scale back

Apply the updated configmap (which sets both `SOURCEBRIDGE_STORAGE_SURREAL_DATABASE` and
`SOURCEBRIDGE_WORKER_SURREAL_DATABASE` to `sourcebridge`), then restore replicas.

```bash
kubectl apply -k deploy/kubernetes/base/
kubectl -n sourcebridge scale deploy sourcebridge-api --replicas=1
kubectl -n sourcebridge scale deploy sourcebridge-worker --replicas=1
kubectl -n sourcebridge rollout status deploy/sourcebridge-api --timeout=120s
```

---

## Section 7 — Verification

After the API pod is healthy, verify that it is reading from the `sourcebridge` database
and that your data is present.

```bash
# After the API is healthy, verify it reads from "sourcebridge" not "main".
kubectl -n sourcebridge exec deploy/sourcebridge-api -- \
  curl -sS http://localhost:8080/healthz
# Expected: 200 OK with the same repo/job counts as before the migration.
```

Cross-check that the repository count in the UI matches what you had before the migration.
If counts are zero, check pod logs (`kubectl -n sourcebridge logs deploy/sourcebridge-api`)
for the active `SOURCEBRIDGE_STORAGE_SURREAL_DATABASE` value.

---

## Section 8 — Rollback

If anything in Sections 5–7 fails, revert the deployment to point back at the original
`main` database. The original data in `main` is untouched by the export-import operation.

```bash
# Revert the configmap to point back at "main" by overriding in an overlay
# or by editing the env vars on the deployments directly:
kubectl -n sourcebridge set env deploy/sourcebridge-api SOURCEBRIDGE_STORAGE_SURREAL_DATABASE=main
kubectl -n sourcebridge set env deploy/sourcebridge-worker SOURCEBRIDGE_WORKER_SURREAL_DATABASE=main
# Scale back up.
kubectl -n sourcebridge scale deploy sourcebridge-api --replicas=1
kubectl -n sourcebridge scale deploy sourcebridge-worker --replicas=1
kubectl -n sourcebridge rollout status deploy/sourcebridge-api --timeout=120s
```

Once the API is back and serving from `main`, investigate the failure before re-attempting
the migration. If the import failed part-way through, the `sourcebridge` database may be in
a partial state — safe to drop and retry: the source data in `main` is the authoritative copy.
