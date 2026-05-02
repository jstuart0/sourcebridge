#!/usr/bin/env sh
# Prints the canonical SourceBridge version string for the current build context.
#
# Output grammar (per thoughts/shared/plans/2026-05-01-version-display-and-flaky-test-fix.md):
#   Tagged release (clean tree)        : <tag>                              e.g. v1.2.3
#   Tagged checkout (dirty tree)       : <tag>-local+g<sha>.dirty           e.g. v1.2.3-local+g956607e.dirty
#   main dev build in CI               : <tag>-dev.<N>+g<sha>[.dirty]       e.g. v0.9.0-rc.3-dev.216+g956607e
#   PR build in CI                     : <tag>-pr<N>+g<sha>[.dirty]         e.g. v0.9.0-rc.3-pr147+g956607e
#   Local clean tree                   : <tag>-local+g<sha>                 e.g. v0.9.0-rc.3-local+g956607e
#   Local dirty tree                   : <tag>-local+g<sha>.dirty           e.g. v0.9.0-rc.3-local+g956607e.dirty
#   No git context                     : 0.0.0-unknown
#   No tags at all (local OR CI)       : 0.0.0-dev.<count>+g<sha>[.dirty]
#
# Honors $GITHUB_EVENT_NAME / $GITHUB_REF / $GITHUB_REF_NAME / $PR_NUMBER / $GITHUB_ACTIONS
# when set by GitHub Actions; falls back to local heuristics otherwise.
#
# CI workflows that invoke this MUST checkout with `fetch-depth: 0` and
# `fetch-tags: true`, otherwise `git describe` falls back to 0.0.0 and
# the resulting versions are uninformative.

set -eu

if ! command -v git >/dev/null 2>&1 || ! git rev-parse --git-dir >/dev/null 2>&1; then
    echo "0.0.0-unknown"
    exit 0
fi

# Refresh index so working-tree state for tracked files is current.
git update-index -q --refresh >/dev/null 2>&1 || :

# Compute dirty FIRST so an exact-tag dirty checkout is not mistaken for a release.
dirty=""
if ! git diff-index --quiet HEAD -- 2>/dev/null; then
    dirty=".dirty"
fi

sha=$(git rev-parse --short=7 HEAD)

# Exact tag + clean tree → release.
if [ -z "${dirty}" ] && tag=$(git describe --tags --exact-match 2>/dev/null); then
    echo "${tag}"
    exit 0
fi

# Base tag for non-release contexts. Track whether one was found so the
# no-tag local path emits the dev.<count> form consistently.
has_tag=1
if base=$(git describe --tags --abbrev=0 2>/dev/null); then
    n=$(git rev-list --count "${base}..HEAD" 2>/dev/null || echo "0")
else
    has_tag=0
    base="0.0.0"
    n=$(git rev-list --count HEAD 2>/dev/null || echo "0")
fi

case "${GITHUB_EVENT_NAME:-}" in
    pull_request)
        # GITHUB_REF for PRs is refs/pull/<num>/merge; GITHUB_REF_NAME is <num>/merge.
        # Workflows MUST also set PR_NUMBER: ${{ github.event.pull_request.number }}
        # so we never depend on parsing alone.
        ref="${GITHUB_REF_NAME:-${GITHUB_REF#refs/pull/}}"
        pr="${PR_NUMBER:-${ref%/merge}}"
        pr="${pr%/head}"
        # Final guard: if pr ended up non-numeric (parsing fell through),
        # tag the build as pr0 so the bug surfaces visibly rather than silently.
        case "${pr}" in
            ''|*[!0-9]*) pr="0" ;;
        esac
        echo "${base}-pr${pr}+g${sha}${dirty}"
        ;;
    push|workflow_dispatch|"")
        if [ "${GITHUB_ACTIONS:-}" = "true" ] || [ "${has_tag}" = "0" ]; then
            # GHA push, OR a no-tag repo (per the grammar table — no-tag → dev.N
            # form everywhere, including local, so the version is at least
            # informative).
            echo "${base}-dev.${n}+g${sha}${dirty}"
        else
            echo "${base}-local+g${sha}${dirty}"
        fi
        ;;
    *)
        echo "${base}-dev.${n}+g${sha}${dirty}"
        ;;
esac
