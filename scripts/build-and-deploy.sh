#!/bin/bash
set -euo pipefail

# Build and (optionally) push SourceBridge container images to GHCR and Docker Hub.
#
# Usage:
#   ./scripts/build-and-deploy.sh [api|web|worker|all] [--push|--no-push] [--help]
#
# Examples:
#   ./scripts/build-and-deploy.sh                # Build all three; push if logged in
#   ./scripts/build-and-deploy.sh api            # Build only api; push if logged in
#   ./scripts/build-and-deploy.sh web --no-push  # Build web only, don't push
#   ./scripts/build-and-deploy.sh all --push     # Build all and push (fail if not logged in)
#
# Environment variables:
#   REGISTRY    — GHCR namespace (default: ghcr.io/sourcebridge-ai)
#   DOCKERHUB   — Docker Hub namespace (default: sourcebridge); set to empty
#                 string to disable Docker Hub tagging entirely
#
# Tag policy:
#   Each image is tagged with sha-<short-sha> AND :latest in both registries.
#   This mirrors what the CI workflow (.github/workflows/build-images.yml)
#   produces on every push to main.
#
# Deployment:
#   This script does NOT deploy. Image rollout is handled by your CD system
#   (Argo CD, Flux, kubectl) operating on a deploy overlay repo. If you need
#   to roll a deployment after a local build, run that against your overlay:
#
#     kubectl -n <ns> rollout restart deployment/<name>
#
#   The opinionated kubectl deploy block that previously lived in this script
#   has been removed so the OSS repo doesn't ship Jay/thor-specific behaviour.

REGISTRY="${REGISTRY:-ghcr.io/sourcebridge-ai}"
DOCKERHUB="${DOCKERHUB-sourcebridge}"  # note: ${DOCKERHUB-...} preserves empty string
TAG="sha-$(git rev-parse --short HEAD)"
COMPONENT="all"
PUSH_MODE="auto"  # auto | force | never

usage() {
  sed -n '3,30p' "$0" | sed 's/^# \?//'
  exit 0
}

for arg in "$@"; do
  case "$arg" in
    api|web|worker|all) COMPONENT="$arg" ;;
    --push)             PUSH_MODE="force" ;;
    --no-push)          PUSH_MODE="never" ;;
    --help|-h)          usage ;;
    *) echo "Unknown argument: $arg"; echo "Run with --help for usage."; exit 1 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Detect Docker Hub login (only when push is desired and DOCKERHUB is non-empty).
DOCKERHUB_AVAILABLE=false
if [ -n "$DOCKERHUB" ]; then
  if docker info 2>/dev/null | grep -q "Username" || \
     grep -q "index.docker.io" "${HOME}/.docker/config.json" 2>/dev/null; then
    DOCKERHUB_AVAILABLE=true
  fi
fi

# Resolve effective push behaviour.
case "$PUSH_MODE" in
  force)
    PUSH_GHCR=true
    PUSH_DOCKERHUB=true
    if [ -n "$DOCKERHUB" ] && [ "$DOCKERHUB_AVAILABLE" = false ]; then
      echo "ERROR: --push specified but not logged in to Docker Hub."
      echo "Run 'docker login' or set DOCKERHUB= to disable Docker Hub."
      exit 1
    fi
    ;;
  never)
    PUSH_GHCR=false
    PUSH_DOCKERHUB=false
    ;;
  auto|*)
    PUSH_GHCR=true                      # always attempt GHCR (assumes you have a GH token loaded)
    PUSH_DOCKERHUB="$DOCKERHUB_AVAILABLE"
    ;;
esac

echo "=== SourceBridge build ==="
echo "Tag:        ${TAG}"
echo "Component:  ${COMPONENT}"
echo "GHCR:       ${REGISTRY} (push: ${PUSH_GHCR})"
if [ -n "$DOCKERHUB" ]; then
  echo "Docker Hub: ${DOCKERHUB} (push: ${PUSH_DOCKERHUB}$([ "$PUSH_DOCKERHUB" = false ] && [ "$PUSH_MODE" = auto ] && echo " — run 'docker login' to enable" || echo "" ))"
else
  echo "Docker Hub: disabled (DOCKERHUB env var is empty)"
fi
echo "Repo root:  ${REPO_ROOT}"
echo ""

build_and_push() {
  local name="$1"
  local dockerfile="$2"

  local tags=(
    "-t" "${REGISTRY}/${name}:${TAG}"
    "-t" "${REGISTRY}/${name}:latest"
  )
  if [ -n "$DOCKERHUB" ]; then
    tags+=(
      "-t" "${DOCKERHUB}/${name}:${TAG}"
      "-t" "${DOCKERHUB}/${name}:latest"
    )
  fi

  echo "--- Building ${name} ---"
  docker build \
    --platform linux/amd64 \
    -f "${REPO_ROOT}/${dockerfile}" \
    "${tags[@]}" \
    "${REPO_ROOT}"

  if [ "$PUSH_GHCR" = true ]; then
    echo "--- Pushing ${name} to ${REGISTRY} ---"
    docker push "${REGISTRY}/${name}:${TAG}"
    docker push "${REGISTRY}/${name}:latest"
  fi

  if [ "$PUSH_DOCKERHUB" = true ] && [ -n "$DOCKERHUB" ]; then
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
  all)
    build_api
    build_web
    build_worker
    ;;
esac

echo ""
echo "=== Build complete ==="
echo "GHCR:       ${REGISTRY}/sourcebridge-{api,web,worker}:${TAG}"
if [ -n "$DOCKERHUB" ]; then
  echo "Docker Hub: ${DOCKERHUB}/sourcebridge-{api,web,worker}:${TAG}"
fi
