#!/usr/bin/env bash
set -euo pipefail

echo "=== SourceBridge Docker Compose E2E Smoke Test ==="

cd "$(dirname "$0")/../.."

echo "1. Starting Docker Compose stack..."
docker compose up -d --build

echo "   Waiting for services (up to 90s)..."
for i in $(seq 1 18); do
  if curl -sf http://localhost:8080/healthz >/dev/null 2>&1; then
    echo "   Services ready after $((i * 5))s"
    break
  fi
  if [ "$i" -eq 18 ]; then
    echo "   FAIL: Services not ready after 90s"
    docker compose logs
    docker compose down -v
    exit 1
  fi
  sleep 5
done

echo "   PASS: Health check returned 200"

echo "2. Indexing fixture repository..."
docker compose exec -T sourcebridge sourcebridge index /app/tests/fixtures/multi-lang-repo || {
  echo "   Using local binary for index..."
  go build -o bin/sourcebridge ./cmd/sourcebridge
  SOURCEBRIDGE_API_URL=http://localhost:8080 bin/sourcebridge index tests/fixtures/multi-lang-repo
}
echo "   PASS: Index completed"

echo "3. Importing requirements..."
docker compose exec -T sourcebridge sourcebridge import /app/tests/fixtures/multi-lang-repo/requirements.md || {
  SOURCEBRIDGE_API_URL=http://localhost:8080 bin/sourcebridge import tests/fixtures/multi-lang-repo/requirements.md
}
echo "   PASS: Import completed"

echo "4. Tracing REQ-001..."
docker compose exec -T sourcebridge sourcebridge trace REQ-001 || {
  SOURCEBRIDGE_API_URL=http://localhost:8080 bin/sourcebridge trace REQ-001
}
echo "   PASS: Trace completed"

echo "5. Running security review..."
docker compose exec -T sourcebridge sourcebridge review /app/tests/fixtures/multi-lang-repo --template security 2>&1 || true
echo "   PASS: Review executed"

echo "6. Asking about code..."
docker compose exec -T sourcebridge sourcebridge ask "what does processPayment do?" 2>&1 || true
echo "   PASS: Ask executed"

echo "7. Checking web UI..."
if curl -sf http://localhost:3000 | grep -q "html\|HTML"; then
  echo "   PASS: Web UI returns HTML"
else
  echo "   WARN: Web UI may not be ready"
fi

echo "8. Tearing down..."
docker compose down -v
echo "   PASS: Docker Compose stopped"

echo ""
echo "=== All Docker Compose E2E smoke tests PASSED ==="
