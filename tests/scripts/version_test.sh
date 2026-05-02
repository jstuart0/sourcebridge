#!/usr/bin/env bash
# Tests for scripts/version.sh
#
# Each case scaffolds a temp git repo with the right state, sets the
# expected GHA env, and asserts the version.sh output matches a regex.
# A few cases reuse the parent repo (the no-git fallback runs in /tmp).
#
# Run via: bash tests/scripts/version_test.sh
#          (or `make test-scripts`)

set -eu

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
VERSION_SH="${REPO_ROOT}/scripts/version.sh"
PASS=0
FAIL=0

if [ ! -x "${VERSION_SH}" ]; then
    echo "FAIL: ${VERSION_SH} is not executable" >&2
    exit 1
fi

# Run version.sh with a clean env (only the vars we set). Always force
# GITHUB_ACTIONS unset by default so case-specific overrides are explicit.
run_clean() {
    env -i HOME="${HOME}" PATH="${PATH}" "$@" "${VERSION_SH}"
}

# Assert the actual output matches a POSIX-extended regex.
assert_regex() {
    local label="$1"
    local actual="$2"
    local pattern="$3"
    if echo "${actual}" | grep -E -q "^${pattern}$"; then
        printf '  PASS: %s → %s\n' "${label}" "${actual}"
        PASS=$((PASS + 1))
    else
        printf '  FAIL: %s\n' "${label}"
        printf '    expected pattern: %s\n' "${pattern}"
        printf '    actual:           %s\n' "${actual}"
        FAIL=$((FAIL + 1))
    fi
}

scaffold_repo() {
    # Initialize a fresh git repo at $1 with at least one commit and an
    # optional tag. Args:  $1=dir  $2=tag (or empty)  $3=extra commits
    local dir="$1"
    local tag="$2"
    local extra="$3"
    (
        cd "${dir}"
        git init -q -b main
        git config user.email "test@example.com"
        git config user.name "Test"
        echo "x" > a.txt
        git add a.txt
        git commit -q -m "init"
        if [ -n "${tag}" ]; then
            git tag "${tag}"
        fi
        i=0
        while [ "${i}" -lt "${extra}" ]; do
            echo "${i}" >> a.txt
            git add a.txt
            git commit -q -m "more ${i}"
            i=$((i + 1))
        done
    )
}

echo "Running scripts/version.sh tests..."

# --- Case 1: no-git fallback ----------------------------------------------
TMP1="$(mktemp -d)"
trap "rm -rf ${TMP1}" EXIT
actual="$(cd "${TMP1}" && env -i HOME="${HOME}" PATH="${PATH}" "${VERSION_SH}")"
assert_regex "no-git fallback" "${actual}" "0\.0\.0-unknown"

# --- Case 2: tag-exact + clean → tag verbatim ------------------------------
TMP2="$(mktemp -d)"
scaffold_repo "${TMP2}" "v1.2.3" 0
actual="$(cd "${TMP2}" && env -i HOME="${HOME}" PATH="${PATH}" "${VERSION_SH}")"
assert_regex "tag-exact + clean" "${actual}" "v1\.2\.3"
rm -rf "${TMP2}"

# --- Case 3: tag-exact + dirty → <tag>-local+g<sha>.dirty -----------------
TMP3="$(mktemp -d)"
scaffold_repo "${TMP3}" "v1.2.3" 0
echo "dirty" >> "${TMP3}/a.txt"
actual="$(cd "${TMP3}" && env -i HOME="${HOME}" PATH="${PATH}" "${VERSION_SH}")"
assert_regex "tag-exact + dirty" "${actual}" "v1\.2\.3-local\+g[0-9a-f]{7}\.dirty"
rm -rf "${TMP3}"

# --- Case 4: main-like, commits past tag, no GHA env → -local form ---------
TMP4="$(mktemp -d)"
scaffold_repo "${TMP4}" "v1.2.3" 5
actual="$(cd "${TMP4}" && env -i HOME="${HOME}" PATH="${PATH}" "${VERSION_SH}")"
assert_regex "local clean past-tag" "${actual}" "v1\.2\.3-local\+g[0-9a-f]{7}"
rm -rf "${TMP4}"

# --- Case 5: GHA push → dev.N form ----------------------------------------
TMP5="$(mktemp -d)"
scaffold_repo "${TMP5}" "v1.2.3" 5
actual="$(cd "${TMP5}" && env -i HOME="${HOME}" PATH="${PATH}" GITHUB_ACTIONS=true GITHUB_EVENT_NAME=push "${VERSION_SH}")"
assert_regex "GHA push past-tag" "${actual}" "v1\.2\.3-dev\.5\+g[0-9a-f]{7}"
rm -rf "${TMP5}"

# --- Case 6: GHA pull_request with PR_NUMBER → pr147 form -----------------
TMP6="$(mktemp -d)"
scaffold_repo "${TMP6}" "v1.2.3" 2
actual="$(cd "${TMP6}" && env -i HOME="${HOME}" PATH="${PATH}" \
    GITHUB_ACTIONS=true GITHUB_EVENT_NAME=pull_request \
    GITHUB_REF=refs/pull/147/merge GITHUB_REF_NAME=147/merge \
    PR_NUMBER=147 \
    "${VERSION_SH}")"
assert_regex "GHA pull_request (PR_NUMBER set)" "${actual}" "v1\.2\.3-pr147\+g[0-9a-f]{7}"
rm -rf "${TMP6}"

# --- Case 7: GHA pull_request without PR_NUMBER → fallback parser ---------
TMP7="$(mktemp -d)"
scaffold_repo "${TMP7}" "v1.2.3" 2
actual="$(cd "${TMP7}" && env -i HOME="${HOME}" PATH="${PATH}" \
    GITHUB_ACTIONS=true GITHUB_EVENT_NAME=pull_request \
    GITHUB_REF=refs/pull/147/merge GITHUB_REF_NAME=147/merge \
    "${VERSION_SH}")"
assert_regex "GHA pull_request (no PR_NUMBER, parsing fallback)" "${actual}" "v1\.2\.3-pr147\+g[0-9a-f]{7}"
rm -rf "${TMP7}"

# --- Case 8: no-tag repo (local) → 0.0.0-dev.<count>+g<sha> ---------------
TMP8="$(mktemp -d)"
scaffold_repo "${TMP8}" "" 2
# 3 commits total (init + 2 extra)
actual="$(cd "${TMP8}" && env -i HOME="${HOME}" PATH="${PATH}" "${VERSION_SH}")"
assert_regex "no-tag local" "${actual}" "0\.0\.0-dev\.3\+g[0-9a-f]{7}"
rm -rf "${TMP8}"

# --- Case 9: non-numeric PR ref guard → pr0 -------------------------------
TMP9="$(mktemp -d)"
scaffold_repo "${TMP9}" "v1.2.3" 1
# Force a malformed ref so the guard kicks in.
actual="$(cd "${TMP9}" && env -i HOME="${HOME}" PATH="${PATH}" \
    GITHUB_ACTIONS=true GITHUB_EVENT_NAME=pull_request \
    GITHUB_REF=refs/pull/garbage/merge GITHUB_REF_NAME=garbage/merge \
    "${VERSION_SH}")"
# After /merge stripping → "garbage" (non-numeric) → pr0.
assert_regex "non-numeric PR ref guard" "${actual}" "v1\.2\.3-pr0\+g[0-9a-f]{7}"
rm -rf "${TMP9}"

echo
echo "Results: ${PASS} passed, ${FAIL} failed"
if [ "${FAIL}" -ne 0 ]; then
    exit 1
fi
