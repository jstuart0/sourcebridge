# Upgrade Guide

## Version Upgrade Workflow

1. Read the release notes for every version between your current and target version
2. Run the pre-upgrade checklist
3. Back up your database
4. Apply the upgrade
5. Verify the deployment
6. Monitor for 30 minutes before declaring success

## Pre-Upgrade Checklist

Run these checks before starting the upgrade:

```bash
# 1. Record current version
curl -s http://localhost:8080/api/v1/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ health { version } }"}' | jq -r '.data.health.version'

# 2. Verify all pods are healthy
kubectl -n sourcebridge get pods
kubectl -n sourcebridge get pods | grep -v Running && echo "WARN: unhealthy pods"

# 3. Check disk space on SurrealDB PVC
kubectl -n sourcebridge exec deploy/surrealdb -- df -h /data

# 4. Back up the database
surreal export --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  pre-upgrade-$(date +%Y%m%d-%H%M%S).surql

# 5. Verify the backup is non-empty
ls -lh pre-upgrade-*.surql

# 6. Verify audit chain integrity
sourcebridge admin audit verify
```

## Applying the Upgrade

### Docker Compose

```bash
# Pull new images
docker compose pull

# Apply with zero downtime (recreates containers one at a time)
docker compose up -d

# Watch for healthy startup
docker compose ps
docker compose logs -f api --since 1m
```

### Helm

```bash
helm repo update

# Review what will change
helm diff upgrade sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge --values production-values.yaml

# Apply
helm upgrade sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge --values production-values.yaml

# Watch rollout
kubectl -n sourcebridge rollout status deployment/sourcebridge-api
kubectl -n sourcebridge rollout status deployment/sourcebridge-worker
```

### kubectl

```bash
# Update image tags in your manifests, then apply
kubectl -n sourcebridge set image deployment/sourcebridge-api \
  sourcebridge-api=ghcr.io/sourcebridge/sourcebridge-api:1.5.0
kubectl -n sourcebridge set image deployment/sourcebridge-worker \
  sourcebridge-worker=ghcr.io/sourcebridge/sourcebridge-worker:1.5.0
kubectl -n sourcebridge set image deployment/sourcebridge-web \
  sourcebridge-web=ghcr.io/sourcebridge/sourcebridge-web:1.5.0

kubectl -n sourcebridge rollout status deployment/sourcebridge-api
```

## Migration Handling

Database schema migrations run automatically when the API server starts. The migration system:

- Tracks applied migrations in a `_migrations` table in SurrealDB
- Runs pending migrations sequentially on startup
- Fails fast if a migration errors (the pod will restart and retry)
- Is idempotent -- safe to run multiple API replicas simultaneously (leader election via Redis lock)

To check migration status:

```bash
# List applied migrations
surreal sql --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  "SELECT * FROM _migrations ORDER BY applied_at"
```

If a migration fails:

```bash
# Check API logs for the error
kubectl -n sourcebridge logs deploy/sourcebridge-api | grep -i migration

# Restore from pre-upgrade backup and investigate
surreal import --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  pre-upgrade-20260315.surql
```

## Upgrading to the Repository Understanding / Understanding-First Model

Versions that introduce the shared repository understanding layer add new artifact and understanding fields such as:

- `generation_mode`
- `renderer_version`
- `understanding_id`
- section-level metadata for AI-backed artifacts

This is still an in-place upgrade. You do **not** need to re-import repositories.

What changes after upgrade:

1. Existing repositories stay indexed.
2. Existing knowledge artifacts remain present.
3. Repository understanding is built lazily on the next qualifying request, or explicitly when a user clicks `Build Understanding`.
4. New artifacts can coexist with older artifacts while the system rebuilds understanding-backed outputs.

### Recommended sequence

1. Upgrade API, worker, and web together.
2. Wait for API startup migrations to finish cleanly.
3. Verify the API can read old artifacts without startup errors.
4. Trigger one `Build Understanding` or one new cliff-notes generation on a known repository.
5. Confirm a `repositoryUnderstanding` record appears and reaches `READY`.

### Legacy artifact normalization

Most installations will upgrade cleanly with migrations alone. If the API fails on startup while reading legacy `ca_knowledge_artifact` rows, normalize the older rows once so the newly added fields are present.

Example repair:

```sql
UPDATE ca_knowledge_artifact SET
  generation_mode = string::is::empty(generation_mode) ? 'understanding_first' : generation_mode,
  renderer_version = string::is::empty(renderer_version) ? 'v1' : renderer_version,
  understanding_id = string::is::empty(understanding_id) ? '' : understanding_id,
  understanding_revision_fp = string::is::empty(understanding_revision_fp) ? '' : understanding_revision_fp
WHERE generation_mode = NONE
   OR renderer_version = NONE
   OR understanding_id = NONE
   OR understanding_revision_fp = NONE;
```

If your version also added section metadata and older rows are missing it, normalize those rows too:

```sql
UPDATE ca_knowledge_section SET
  metadata = object::is::empty(metadata) ? {} : metadata
WHERE metadata = NONE;
```

Run those repairs only if startup or query behavior shows legacy-row incompatibility. Do not wipe repositories or artifacts as a first response.

### Post-upgrade verification for understanding-aware installs

After the deploy:

```bash
# Confirm repositories are still queryable
curl -s http://localhost:8080/api/v1/graphql \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{"query":"{ repositories { id name } }"}' | jq .

# Confirm the worker is connected
curl -s http://localhost:8080/api/v1/admin/llm/monitor | jq '.worker_connected'
```

Then in the UI:

1. Open a repository page.
2. Click `Build Understanding` if no understanding record exists yet.
3. Refresh the page after the job starts.
4. Verify the understanding panel shows a real record with:
   - stage progress
   - node counts
   - `READY` when complete

If `Build Understanding` flips back to idle with no visible record, verify that the `Repository.repositoryUnderstanding` GraphQL field resolves correctly in your deployed version before investigating the worker.

## Rollback Procedure

### Helm Rollback

```bash
# List release history
helm history sourcebridge --namespace sourcebridge

# Rollback to previous revision
helm rollback sourcebridge --namespace sourcebridge

# Verify
kubectl -n sourcebridge get pods
```

### Docker Compose Rollback

Edit `docker-compose.yml` to pin the previous image tags, then:

```bash
docker compose up -d
```

### kubectl Rollback

```bash
kubectl -n sourcebridge rollout undo deployment/sourcebridge-api
kubectl -n sourcebridge rollout undo deployment/sourcebridge-worker
kubectl -n sourcebridge rollout undo deployment/sourcebridge-web
```

### Database Rollback

If the new version applied a schema migration, rolling back the application without rolling back the database may cause errors. Restore from your pre-upgrade backup:

```bash
# Scale down all workloads
kubectl -n sourcebridge scale deployment --all --replicas=0

# Restore database
surreal import --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  pre-upgrade-20260315.surql

# Scale back up with old image tags
kubectl -n sourcebridge scale deployment --all --replicas=1
```

## Version Compatibility Matrix

| SourceBridge.ai Version | SurrealDB | Redis | Kubernetes | Helm Chart |
|-------------------|-----------|-------|------------|------------|
| 1.5.x             | 2.0+      | 7.x   | 1.26+      | 0.5.x      |
| 1.4.x             | 2.0+      | 7.x   | 1.24+      | 0.4.x      |
| 1.3.x             | 1.5+      | 7.x   | 1.24+      | 0.3.x      |
| 1.2.x             | 1.5+      | 6.x+  | 1.24+      | 0.2.x      |
| 1.1.x             | 1.4+      | 6.x+  | 1.23+      | 0.1.x      |

**Supported upgrade paths:** You can upgrade one minor version at a time (e.g., 1.3 to 1.4, then 1.4 to 1.5). Skipping minor versions is not supported.

## Breaking Changes Policy

SourceBridge.ai follows semantic versioning:

- **Patch releases (1.4.x):** Bug fixes only. No migrations, no breaking changes. Safe to apply without a backup (but always back up anyway).
- **Minor releases (1.x.0):** New features, possible schema migrations. Backwards-compatible API. Always back up before upgrading.
- **Major releases (x.0.0):** May include breaking API changes, required data migrations, or dependency version bumps. Read the migration guide in the release notes.

Breaking changes are announced at least one minor version in advance via deprecation warnings in the API response headers (`X-SourceBridge.ai-Deprecated`) and in the server logs.

## Post-Upgrade Verification

After the upgrade completes:

```bash
# 1. Confirm new version
curl -s http://localhost:8080/api/v1/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ health { version status } }"}' | jq .

# 2. Run readiness check
curl -s http://localhost:8080/readyz

# 3. Verify a GraphQL query works end-to-end
curl -s http://localhost:8080/api/v1/graphql \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{"query":"{ repositories { id name } }"}' | jq .

# 4. Check audit chain is still valid
sourcebridge admin audit verify

# 5. Confirm no error spikes in logs
kubectl -n sourcebridge logs deploy/sourcebridge-api --since 10m | grep -c ERROR
```
