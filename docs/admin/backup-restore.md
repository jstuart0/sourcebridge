# Backup and Restore

## SurrealDB Backup

### Full Database Export

SurrealDB supports native export to a portable format:

```bash
# Export from a running instance
surreal export --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  backup-$(date +%Y%m%d-%H%M%S).surql
```

From Docker Compose:

```bash
docker compose exec surrealdb surreal export \
  --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  /data/backup.surql

# Copy the file out of the container
docker compose cp surrealdb:/data/backup.surql ./backup-$(date +%Y%m%d).surql
```

### Volume-Level Backup

For file-based SurrealDB (the default), you can snapshot the data directory:

```bash
# Stop writes (optional, for consistency)
docker compose stop api worker

# Copy the data volume
docker run --rm -v sourcebridge_surreal_data:/source -v $(pwd):/backup \
  alpine tar czf /backup/surreal-data-$(date +%Y%m%d).tar.gz -C /source .

# Resume
docker compose start api worker
```

### Kubernetes Backup

```bash
# Scale down writers for a consistent snapshot
kubectl -n sourcebridge scale deployment/sourcebridge-api --replicas=0
kubectl -n sourcebridge scale deployment/sourcebridge-worker --replicas=0

# Port-forward to SurrealDB and export
kubectl -n sourcebridge port-forward svc/surrealdb 8000:8000 &
surreal export --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  backup-$(date +%Y%m%d).surql

# Scale back up
kubectl -n sourcebridge scale deployment/sourcebridge-api --replicas=2
kubectl -n sourcebridge scale deployment/sourcebridge-worker --replicas=1
```

## Restore

### SurrealDB Import

```bash
# Import into a fresh or existing database
surreal import --conn http://localhost:8000 \
  --user root --pass ${SURREAL_ROOT_PASS} \
  --ns sourcebridge --db production \
  backup-20260315.surql
```

### Volume Restore

```bash
docker compose down
docker volume rm sourcebridge_surreal_data
docker volume create sourcebridge_surreal_data

docker run --rm -v sourcebridge_surreal_data:/target -v $(pwd):/backup \
  alpine tar xzf /backup/surreal-data-20260315.tar.gz -C /target

docker compose up -d
```

## Repository Cache Rehydration

After restoring the database, repository index caches are intact in SurrealDB. However, if you need to reindex:

```bash
# Trigger reindex for a specific repository
curl -X POST http://localhost:8080/api/v1/repos/{repo-id}/reindex \
  -H "Authorization: Bearer ${ADMIN_TOKEN}"

# Trigger reindex for all repositories in a tenant
curl -X POST http://localhost:8080/api/v1/admin/reindex-all \
  -H "Authorization: Bearer ${ADMIN_TOKEN}"
```

The worker processes will pick up reindex jobs from the Redis queue. Monitor progress:

```bash
curl http://localhost:8080/api/v1/admin/jobs \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" | jq '.pending, .active'
```

## Tenant Data Export/Import

SourceBridge.ai provides a built-in export/import system for migrating tenant data between instances. Exports include repositories, requirements, links, and settings -- but no source code.

### Export a Tenant

```bash
# Via CLI
sourcebridge admin export --tenant acme-corp --output acme-export.json

# Via API
curl http://localhost:8080/api/v1/admin/export?tenant=acme-corp \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -o acme-export.json
```

### Validate Before Import

```bash
sourcebridge admin validate --input acme-export.json
```

This checks version compatibility, duplicate IDs, referential integrity, and orphaned records without writing anything.

### Import to Target Instance

```bash
# Import into an existing tenant
sourcebridge admin import --input acme-export.json --tenant acme-corp-new

# Via API
curl -X POST http://localhost:8080/api/v1/admin/import?tenant=acme-corp-new \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  -d @acme-export.json
```

## Disaster Recovery Procedure

1. **Assess the failure.** Determine whether you lost the database, the full cluster, or a single service.

2. **Provision infrastructure.** Stand up a fresh environment using Docker Compose or Kubernetes manifests.

3. **Restore SurrealDB.** Import the most recent backup:
   ```bash
   surreal import --conn http://localhost:8000 \
     --user root --pass ${SURREAL_ROOT_PASS} \
     --ns sourcebridge --db production \
     backup-latest.surql
   ```

4. **Restore secrets.** Re-create `SOURCEBRIDGE_JWT_SECRET`, `STRIPE_WEBHOOK_SECRET`, and LLM API keys. If the JWT secret changes, all existing user sessions will be invalidated.

5. **Start services.** Bring up the API, web, and worker containers.

6. **Verify health.**
   ```bash
   curl http://localhost:8080/readyz
   curl http://localhost:8080/api/v1/graphql \
     -H 'Content-Type: application/json' \
     -d '{"query":"{ health { status } }"}'
   ```

7. **Reindex if needed.** If the backup is older than the last code change, trigger a full reindex.

8. **Verify audit chain.**
   ```bash
   sourcebridge admin audit verify
   ```

## Backup Schedule Recommendations

| Environment | Frequency | Retention | Method |
|-------------|-----------|-----------|--------|
| Production  | Every 6 hours | 30 days | SurrealDB export + volume snapshot |
| Staging     | Daily | 7 days | SurrealDB export |
| Development | Manual | 3 days | Volume snapshot |

Automate with cron:

```bash
# /etc/cron.d/sourcebridge-backup
0 */6 * * * root /opt/sourcebridge/scripts/backup.sh >> /var/log/sourcebridge-backup.log 2>&1
```

## Testing Your Backup

Run a restore test monthly:

```bash
# Spin up a throwaway instance
docker compose -f docker-compose.test.yml up -d surrealdb

# Import the latest backup
surreal import --conn http://localhost:9000 \
  --user root --pass testpass \
  --ns sourcebridge --db production \
  backup-latest.surql

# Run a smoke test query
surreal sql --conn http://localhost:9000 \
  --user root --pass testpass \
  --ns sourcebridge --db production \
  "SELECT count() FROM repository GROUP ALL"

# Tear down
docker compose -f docker-compose.test.yml down -v
```

Document the result and timestamp. If the restore fails, fix the backup pipeline before the next production incident.
