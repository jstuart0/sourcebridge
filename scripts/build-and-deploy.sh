#!/bin/bash
set -euo pipefail

# Build, push, and deploy SourceBridge to thor cluster
# Usage: ./scripts/build-and-deploy.sh [component] [--no-deploy]
#
# Examples:
#   ./scripts/build-and-deploy.sh          # Build all, deploy
#   ./scripts/build-and-deploy.sh api      # Build only api, deploy
#   ./scripts/build-and-deploy.sh web      # Build only web, deploy
#   ./scripts/build-and-deploy.sh worker   # Build only worker, deploy
#   ./scripts/build-and-deploy.sh --no-deploy  # Build all, skip deploy

REGISTRY="192.168.10.222:30500"
TAG="sha-$(git rev-parse --short HEAD)"
COMPONENT="${1:-all}"
NO_DEPLOY=false

for arg in "$@"; do
  if [ "$arg" = "--no-deploy" ]; then
    NO_DEPLOY=true
  fi
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== SourceBridge Build & Deploy ==="
echo "Registry: ${REGISTRY}"
echo "Tag:      ${TAG}"
echo "Component: ${COMPONENT}"
echo "Repo root: ${REPO_ROOT}"
echo ""

# Verify kubectl context
CONTEXT=$(kubectl config current-context)
if [ "$CONTEXT" != "thor" ]; then
  echo "ERROR: kubectl context is '${CONTEXT}', expected 'thor'"
  echo "Run: kubectl config use-context thor"
  exit 1
fi

build_api() {
  echo "--- Building sourcebridge-api ---"
  docker build \
    --platform linux/amd64 \
    -f "${REPO_ROOT}/deploy/docker/Dockerfile.sourcebridge" \
    -t "${REGISTRY}/sourcebridge-api:${TAG}" \
    -t "${REGISTRY}/sourcebridge-api:latest" \
    "${REPO_ROOT}"
  echo "--- Pushing sourcebridge-api ---"
  docker push "${REGISTRY}/sourcebridge-api:${TAG}"
  docker push "${REGISTRY}/sourcebridge-api:latest"
}

build_web() {
  echo "--- Building sourcebridge-web ---"
  docker build \
    --platform linux/amd64 \
    -f "${REPO_ROOT}/deploy/docker/Dockerfile.web" \
    -t "${REGISTRY}/sourcebridge-web:${TAG}" \
    -t "${REGISTRY}/sourcebridge-web:latest" \
    "${REPO_ROOT}"
  echo "--- Pushing sourcebridge-web ---"
  docker push "${REGISTRY}/sourcebridge-web:${TAG}"
  docker push "${REGISTRY}/sourcebridge-web:latest"
}

build_worker() {
  echo "--- Building sourcebridge-worker ---"
  docker build \
    --platform linux/amd64 \
    -f "${REPO_ROOT}/deploy/docker/Dockerfile.worker" \
    -t "${REGISTRY}/sourcebridge-worker:${TAG}" \
    -t "${REGISTRY}/sourcebridge-worker:latest" \
    "${REPO_ROOT}"
  echo "--- Pushing sourcebridge-worker ---"
  docker push "${REGISTRY}/sourcebridge-worker:${TAG}"
  docker push "${REGISTRY}/sourcebridge-worker:latest"
}

case "$COMPONENT" in
  api)    build_api ;;
  web)    build_web ;;
  worker) build_worker ;;
  all|--no-deploy)
    build_api
    build_web
    build_worker
    ;;
  *)
    echo "Unknown component: ${COMPONENT}"
    echo "Usage: $0 [api|web|worker|all] [--no-deploy]"
    exit 1
    ;;
esac

if [ "$NO_DEPLOY" = true ]; then
  echo ""
  echo "=== Build complete (deploy skipped) ==="
  echo "Images pushed with tag: ${TAG}"
  exit 0
fi

echo ""
echo "--- Restarting deployments ---"

DEPLOYMENTS="sourcebridge-api sourcebridge-web sourcebridge-worker"
for DEPLOY in $DEPLOYMENTS; do
  # Only restart if we built that component (or all)
  case "$COMPONENT" in
    all|--no-deploy) ;; # restart all
    api)    [ "$DEPLOY" != "sourcebridge-api" ] && continue ;;
    web)    [ "$DEPLOY" != "sourcebridge-web" ] && continue ;;
    worker) [ "$DEPLOY" != "sourcebridge-worker" ] && continue ;;
  esac

  echo "Restarting deployment/${DEPLOY} in sourcebridge"
  kubectl -n sourcebridge rollout restart "deployment/${DEPLOY}" 2>/dev/null || \
    echo "  Warning: deployment/${DEPLOY} not found (may not be deployed yet)"
done

echo ""
echo "--- Waiting for rollouts ---"
for DEPLOY in $DEPLOYMENTS; do
  case "$COMPONENT" in
    all|--no-deploy) ;;
    api)    [ "$DEPLOY" != "sourcebridge-api" ] && continue ;;
    web)    [ "$DEPLOY" != "sourcebridge-web" ] && continue ;;
    worker) [ "$DEPLOY" != "sourcebridge-worker" ] && continue ;;
  esac

  kubectl -n sourcebridge rollout status "deployment/${DEPLOY}" --timeout=300s 2>/dev/null || \
    echo "  Warning: rollout status check failed for ${DEPLOY}"
done

echo ""
echo "=== Deploy complete ==="
echo "Images: ${REGISTRY}/sourcebridge-{api,web,worker}:${TAG}"
echo "ArgoCD will reconcile any manifest changes automatically."
