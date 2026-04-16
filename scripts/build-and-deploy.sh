#!/bin/bash
set -euo pipefail

# Build, push, and deploy SourceBridge to a Kubernetes cluster
# Usage: ./scripts/build-and-deploy.sh [component] [--no-deploy]
#
# Examples:
#   ./scripts/build-and-deploy.sh          # Build all, deploy
#   ./scripts/build-and-deploy.sh api      # Build only api, deploy
#   ./scripts/build-and-deploy.sh web      # Build only web, deploy
#   ./scripts/build-and-deploy.sh worker   # Build only worker, deploy
#   ./scripts/build-and-deploy.sh --no-deploy  # Build all, skip deploy
#
# Environment variables:
#   REGISTRY    — Primary container registry (default: ghcr.io/sourcebridge-ai)
#   DOCKERHUB   — Docker Hub org/user for mirroring (default: sourcebridge)
#   KUBE_CONTEXT — kubectl context to use (default: current context)
#   NAMESPACE   — Kubernetes namespace (default: sourcebridge)

REGISTRY="${REGISTRY:-ghcr.io/sourcebridge-ai}"
DOCKERHUB="${DOCKERHUB:-sourcebridge}"
NAMESPACE="${NAMESPACE:-sourcebridge}"
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

# Check if Docker Hub push is possible (logged in)
DOCKERHUB_AVAILABLE=false
if docker info 2>/dev/null | grep -q "Username" || grep -q "index.docker.io" ~/.docker/config.json 2>/dev/null; then
  DOCKERHUB_AVAILABLE=true
fi

echo "=== SourceBridge Build & Deploy ==="
echo "Registry:   ${REGISTRY}"
echo "Docker Hub: ${DOCKERHUB} ($([ "$DOCKERHUB_AVAILABLE" = true ] && echo "enabled" || echo "skipped — run 'docker login' first"))"
echo "Tag:        ${TAG}"
echo "Component:  ${COMPONENT}"
echo "Namespace:  ${NAMESPACE}"
echo "Repo root:  ${REPO_ROOT}"
echo ""

# Verify kubectl context if KUBE_CONTEXT is set
if [ -n "${KUBE_CONTEXT:-}" ]; then
  CONTEXT=$(kubectl config current-context)
  if [ "$CONTEXT" != "$KUBE_CONTEXT" ]; then
    echo "ERROR: kubectl context is '${CONTEXT}', expected '${KUBE_CONTEXT}'"
    echo "Run: kubectl config use-context ${KUBE_CONTEXT}"
    exit 1
  fi
fi

# Build, tag for both registries, and push.
# Docker Hub tags are always applied (so the image is ready to push),
# but only pushed when DOCKERHUB_AVAILABLE=true.
build_and_push() {
  local name="$1"
  local dockerfile="$2"

  echo "--- Building ${name} ---"
  docker build \
    --platform linux/amd64 \
    -f "${REPO_ROOT}/${dockerfile}" \
    -t "${REGISTRY}/${name}:${TAG}" \
    -t "${REGISTRY}/${name}:latest" \
    -t "${DOCKERHUB}/${name}:${TAG}" \
    -t "${DOCKERHUB}/${name}:latest" \
    "${REPO_ROOT}"

  echo "--- Pushing ${name} to ${REGISTRY} ---"
  docker push "${REGISTRY}/${name}:${TAG}"
  docker push "${REGISTRY}/${name}:latest"

  if [ "$DOCKERHUB_AVAILABLE" = true ]; then
    echo "--- Pushing ${name} to Docker Hub (${DOCKERHUB}) ---"
    docker push "${DOCKERHUB}/${name}:${TAG}"
    docker push "${DOCKERHUB}/${name}:latest"
  fi
}

build_api()    { build_and_push "sourcebridge-api"    "deploy/docker/Dockerfile.sourcebridge"; }
build_web()    { build_and_push "sourcebridge-web"    "deploy/docker/Dockerfile.web"; }
build_worker() { build_and_push "sourcebridge-worker" "deploy/docker/Dockerfile.worker"; }

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
  echo "GHCR:       ${REGISTRY}/sourcebridge-{api,web,worker}:${TAG}"
  if [ "$DOCKERHUB_AVAILABLE" = true ]; then
    echo "Docker Hub: ${DOCKERHUB}/sourcebridge-{api,web,worker}:${TAG}"
  fi
  exit 0
fi

echo ""
echo "--- Updating deployments to image tag ${TAG} ---"

DEPLOYMENTS="sourcebridge-api sourcebridge-web sourcebridge-worker"
for DEPLOY in $DEPLOYMENTS; do
  # Only restart if we built that component (or all)
  case "$COMPONENT" in
    all|--no-deploy) ;; # restart all
    api)    [ "$DEPLOY" != "sourcebridge-api" ] && continue ;;
    web)    [ "$DEPLOY" != "sourcebridge-web" ] && continue ;;
    worker) [ "$DEPLOY" != "sourcebridge-worker" ] && continue ;;
  esac

  IMAGE="${REGISTRY}/${DEPLOY}:${TAG}"
  echo "Setting deployment/${DEPLOY} container ${DEPLOY} to ${IMAGE}"
  if ! kubectl -n "${NAMESPACE}" set image "deployment/${DEPLOY}" "${DEPLOY}=${IMAGE}" >/dev/null 2>&1; then
    echo "  Warning: set image failed for deployment/${DEPLOY}; attempting rollout restart"
    kubectl -n "${NAMESPACE}" rollout restart "deployment/${DEPLOY}" 2>/dev/null || \
      echo "  Warning: deployment/${DEPLOY} not found (may not be deployed yet)"
  fi
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

  kubectl -n "${NAMESPACE}" rollout status "deployment/${DEPLOY}" --timeout=300s 2>/dev/null || \
    echo "  Warning: rollout status check failed for ${DEPLOY}"
done

echo ""
echo "=== Deploy complete ==="
echo "GHCR:       ${REGISTRY}/sourcebridge-{api,web,worker}:${TAG}"
if [ "$DOCKERHUB_AVAILABLE" = true ]; then
  echo "Docker Hub: ${DOCKERHUB}/sourcebridge-{api,web,worker}:${TAG}"
fi
