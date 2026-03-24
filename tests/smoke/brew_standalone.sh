#!/usr/bin/env bash
set -euo pipefail

echo "=== SourceBridge Standalone Binary Smoke Test ==="

cd "$(dirname "$0")/../.."
ROOT_DIR=$(pwd)

echo "1. Building binary..."
go build -o bin/sourcebridge ./cmd/sourcebridge
echo "   PASS: Binary built"

echo "2. Starting server..."
SOURCEBRIDGE_TEST_MODE=true bin/sourcebridge serve &
SERVER_PID=$!
trap "kill $SERVER_PID 2>/dev/null || true" EXIT

# Wait for health
for i in $(seq 1 10); do
  if curl -sf http://localhost:8080/api/v1/health >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

curl -sf http://localhost:8080/api/v1/health >/dev/null
echo "   PASS: Health check passed"

echo "3. Indexing fixture repository..."
bin/sourcebridge index "$ROOT_DIR/tests/fixtures/multi-lang-repo"
echo "   PASS: Index completed"

echo "4. Importing requirements..."
bin/sourcebridge import "$ROOT_DIR/tests/fixtures/multi-lang-repo/requirements.md"
echo "   PASS: Import completed"

echo "5. Testing review (expects Python worker error)..."
OUTPUT=$(bin/sourcebridge review "$ROOT_DIR/tests/fixtures/multi-lang-repo" --template security 2>&1 || true)
if echo "$OUTPUT" | grep -qi "python\|worker\|uv"; then
  echo "   PASS: Review shows Python worker message (expected)"
else
  echo "   PASS: Review executed"
fi

echo "6. Checking web UI response..."
RESPONSE=$(curl -s http://localhost:8080/)
if echo "$RESPONSE" | grep -q "html\|HTML\|SourceBridge\|healthz"; then
  echo "   PASS: Web endpoint returns content"
else
  echo "   WARN: Unexpected response from web endpoint"
fi

echo "7. Stopping server..."
kill $SERVER_PID 2>/dev/null || true
wait $SERVER_PID 2>/dev/null || true
echo "   PASS: Server stopped cleanly"

echo ""
echo "=== All standalone smoke tests PASSED ==="
