# SurrealDB Backup & Restore

## Prerequisites

- `kubectl` with context set to your cluster
- Access to the `sourcebridge` namespace

## Backup

Export all data from SurrealDB:

```bash
# Port-forward SurrealDB
kubectl -n sourcebridge port-forward svc/surrealdb 8000:8000 &

# Export (requires surreal CLI or curl)
curl -X POST http://localhost:8000/export \
  -H "Accept: application/octet-stream" \
  -H "NS: sourcebridge" \
  -H "DB: main" \
  -u root:$(kubectl -n sourcebridge get secret sourcebridge-secrets -o jsonpath='{.data.SOURCEBRIDGE_STORAGE_SURREAL_PASS}' | base64 -d) \
  -o surrealdb-backup-$(date +%Y%m%d-%H%M%S).surql

# Stop port-forward
kill %1
```

## Restore

Import a backup into SurrealDB:

```bash
# Port-forward SurrealDB
kubectl -n sourcebridge port-forward svc/surrealdb 8000:8000 &

# Import
curl -X POST http://localhost:8000/import \
  -H "Content-Type: application/octet-stream" \
  -H "NS: sourcebridge" \
  -H "DB: main" \
  -u root:$(kubectl -n sourcebridge get secret sourcebridge-secrets -o jsonpath='{.data.SOURCEBRIDGE_STORAGE_SURREAL_PASS}' | base64 -d) \
  --data-binary @surrealdb-backup-TIMESTAMP.surql

# Stop port-forward
kill %1
```

## Automated Backup (CronJob)

For scheduled backups, deploy the backup CronJob:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: surrealdb-backup
  namespace: sourcebridge
spec:
  schedule: "0 2 * * *"  # Daily at 2am
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: backup
              image: curlimages/curl:latest
              command:
                - /bin/sh
                - -c
                - |
                  TIMESTAMP=$(date +%Y%m%d-%H%M%S)
                  curl -X POST http://surrealdb.sourcebridge.svc.cluster.local:8000/export \
                    -H "Accept: application/octet-stream" \
                    -H "NS: sourcebridge" \
                    -H "DB: main" \
                    -u root:${SURREAL_PASS} \
                    -o /backups/surrealdb-${TIMESTAMP}.surql
              env:
                - name: SURREAL_PASS
                  valueFrom:
                    secretKeyRef:
                      name: sourcebridge-secrets
                      key: SOURCEBRIDGE_STORAGE_SURREAL_PASS
              volumeMounts:
                - name: backup-volume
                  mountPath: /backups
          restartPolicy: OnFailure
          volumes:
            - name: backup-volume
              persistentVolumeClaim:
                claimName: surrealdb-backups
```

## Verification

After restore, verify data integrity:

```bash
# Check schema version
curl -X POST http://localhost:8000/sql \
  -H "Content-Type: application/json" \
  -H "NS: sourcebridge" -H "DB: main" \
  -u root:PASSWORD \
  -d '{"query": "SELECT * FROM schema_version;"}'

# Check table counts
curl -X POST http://localhost:8000/sql \
  -H "Content-Type: application/json" \
  -H "NS: sourcebridge" -H "DB: main" \
  -u root:PASSWORD \
  -d '{"query": "SELECT count() FROM ca_repository GROUP ALL; SELECT count() FROM ca_symbol GROUP ALL;"}'
```
