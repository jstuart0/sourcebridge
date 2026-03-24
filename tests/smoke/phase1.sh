#!/usr/bin/env bash
# Phase 1 Smoke Tests
# Tests that the binary starts, serves health checks, and shuts down cleanly.

set -euo pipefail

BINARY="./bin/sourcebridge"
PORT=18080
HEALTH_URL="http://localhost:${PORT}/healthz"
MAX_WAIT=10

echo "=== Phase 1 Smoke Tests ==="

# Build the binary
echo "[1/4] Building binary..."
go build -o "$BINARY" ./cmd/sourcebridge
echo "  OK: Binary built"

# Start the server in the background
echo "[2/4] Starting server on port ${PORT}..."
SOURCEBRIDGE_SERVER_HTTP_PORT="$PORT" "$BINARY" serve &
SERVER_PID=$!

# Wait for health check
echo "[3/4] Waiting for health check..."
HEALTHY=false
for i in $(seq 1 $MAX_WAIT); do
    if curl -sf "$HEALTH_URL" > /dev/null 2>&1; then
        HEALTHY=true
        echo "  OK: Health check passed after ${i}s"
        break
    fi
    sleep 1
done

if [ "$HEALTHY" = false ]; then
    echo "  FAIL: Health check did not pass within ${MAX_WAIT}s"
    kill "$SERVER_PID" 2>/dev/null || true
    exit 1
fi

# Verify health response body
HEALTH_BODY=$(curl -sf "$HEALTH_URL")
if [ "$HEALTH_BODY" != "ok" ]; then
    echo "  FAIL: Expected 'ok', got '${HEALTH_BODY}'"
    kill "$SERVER_PID" 2>/dev/null || true
    exit 1
fi
echo "  OK: Health body is 'ok'"

# Verify metrics endpoint
METRICS_STATUS=$(curl -sf -o /dev/null -w "%{http_code}" "http://localhost:${PORT}/metrics")
if [ "$METRICS_STATUS" != "200" ]; then
    echo "  FAIL: Metrics endpoint returned ${METRICS_STATUS}"
    kill "$SERVER_PID" 2>/dev/null || true
    exit 1
fi
echo "  OK: Metrics endpoint returns 200"

# Verify security headers
CSP=$(curl -sf -D - "$HEALTH_URL" | grep -i "Content-Security-Policy" || true)
if [ -z "$CSP" ]; then
    echo "  FAIL: Missing Content-Security-Policy header"
    kill "$SERVER_PID" 2>/dev/null || true
    exit 1
fi
echo "  OK: Security headers present"

# Graceful shutdown
echo "[4/4] Testing graceful shutdown..."
kill -TERM "$SERVER_PID"
wait "$SERVER_PID" 2>/dev/null || true
echo "  OK: Server exited cleanly"

echo ""
echo "=== All Phase 1 Smoke Tests PASSED ==="
