#!/usr/bin/env bash
#
# Deploy preflight for SourceBridge homelab cluster (thor).
#
# Run BEFORE pushing to origin/main on a release that touches drain semantics,
# manifests, or any path that triggers a pod restart. Verifies:
#
#   1. kubectl is pointed at the right cluster
#   2. Argo CD application `sourcebridge` is currently Synced + Healthy
#   3. Argo CD Image Updater is running (or explicitly scaled to 0 with a note)
#   4. No Living Wiki cold-start is in flight (FIRST-ROLLOUT RISK — see below)
#   5. GHA build-images workflow is green on origin/main HEAD
#
# FIRST-ROLLOUT RISK:
#   The very first rollout that ships drain primitives (CA-142 and onward) is
#   itself unprotected — the OLD pod does not have drain code, so its SIGTERM
#   path is the pre-CA-142 30-second grace window. Any in-flight Living Wiki
#   cold-start on the old pod dies. This is unavoidable until the new image
#   is running. Subsequent rollouts use the new drain primitives.
#
# Exit codes:
#   0  — safe to push
#   1  — unsafe (active cold-start, app degraded, image-updater unexpected)
#   2  — preflight could not determine state (kubectl unreachable, etc.)
#
# Usage: scripts/deploy-preflight.sh
#        scripts/deploy-preflight.sh --allow-active-jobs   (override #4)
#

set -uo pipefail

ALLOW_ACTIVE_JOBS=0
if [[ "${1:-}" == "--allow-active-jobs" ]]; then
  ALLOW_ACTIVE_JOBS=1
fi

EXIT=0
warn() { printf "  \033[33mWARN\033[0m  %s\n" "$1" >&2; EXIT=1; }
fail() { printf "  \033[31mFAIL\033[0m  %s\n" "$1" >&2; EXIT=1; }
ok()   { printf "  \033[32mOK\033[0m    %s\n" "$1"; }

printf "\n== SourceBridge deploy preflight ==\n\n"

# 1. Cluster context
ctx=$(kubectl config current-context 2>/dev/null || echo "<none>")
if [[ "$ctx" == "thor" || "$ctx" == "kubernetes-admin@kubernetes" ]]; then
  ok "kubectl context: $ctx"
else
  fail "kubectl context is '$ctx' — expected 'thor' or 'kubernetes-admin@kubernetes'. Run: kubectl config use-context thor"
  printf "\nAborting; cannot proceed without correct cluster.\n"
  exit 2
fi

# 2. Argo CD application status
sync=$(kubectl -n argocd get applications.argoproj.io sourcebridge -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "")
health=$(kubectl -n argocd get applications.argoproj.io sourcebridge -o jsonpath='{.status.health.status}' 2>/dev/null || echo "")
if [[ "$sync" == "Synced" && "$health" == "Healthy" ]]; then
  ok "Argo CD app: Synced + Healthy"
else
  fail "Argo CD app: sync=$sync health=$health (expected Synced + Healthy)"
fi

# 3. Image Updater
iu_replicas=$(kubectl -n argocd get deploy argocd-image-updater-controller -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "?")
case "$iu_replicas" in
  1) ok "Image Updater: replicas=1 (write-back will fire on next poll)" ;;
  0) warn "Image Updater: replicas=0 (write-back paused — manual SHA bump required)" ;;
  *) fail "Image Updater: replicas=$iu_replicas (unexpected)" ;;
esac

# 4. Active Living Wiki cold-starts on any API pod (last 5 minutes)
api_pods=$(kubectl -n sourcebridge get pods -l app.kubernetes.io/name=sourcebridge-api -o name 2>/dev/null)
if [[ -z "$api_pods" ]]; then
  fail "No sourcebridge-api pods found"
else
  active_jobs=$(kubectl -n sourcebridge logs -l app.kubernetes.io/name=sourcebridge-api --tail=500 --since=5m 2>/dev/null \
    | grep -cE 'event=(coldstart_started|debug_slow_job_started)' || true)
  finished_jobs=$(kubectl -n sourcebridge logs -l app.kubernetes.io/name=sourcebridge-api --tail=500 --since=5m 2>/dev/null \
    | grep -cE 'event=(coldstart_complete|debug_slow_job_done|debug_slow_job_cancelled)' || true)
  in_flight=$((active_jobs - finished_jobs))
  if (( in_flight <= 0 )); then
    ok "No in-flight Living Wiki cold-starts (started=$active_jobs, finished=$finished_jobs)"
  elif (( ALLOW_ACTIVE_JOBS == 1 )); then
    warn "$in_flight in-flight cold-start(s) — proceeding because --allow-active-jobs"
  else
    fail "$in_flight in-flight Living Wiki cold-start(s) — wait for completion or pass --allow-active-jobs"
    printf "       (Active jobs will be killed by the FIRST rollout because old pod has no drain code.)\n"
  fi
fi

# 5. GHA build status on origin/main HEAD
remote_head=$(git rev-parse --short origin/main 2>/dev/null || echo "?")
if command -v gh >/dev/null 2>&1; then
  build_status=$(gh run list --workflow build-images.yml --branch main --limit 1 --json conclusion --jq '.[0].conclusion' 2>/dev/null || echo "?")
  case "$build_status" in
    success) ok "Last GHA build-images run on main: success (origin/main = $remote_head)" ;;
    in_progress|"") warn "GHA build-images run is in progress or unknown" ;;
    *) warn "Last GHA build-images run: $build_status — investigate before pushing" ;;
  esac
else
  warn "gh CLI not installed — skipping GHA check"
fi

# Summary
printf "\n"
if (( EXIT == 0 )); then
  printf "\033[32mAll checks passed — safe to push.\033[0m\n\n"
else
  printf "\033[31mPreflight raised concerns — review above before pushing.\033[0m\n"
  printf "Override with --allow-active-jobs only if you accept the first-rollout risk.\n\n"
fi
exit $EXIT
