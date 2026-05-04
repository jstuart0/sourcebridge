#!/usr/bin/env bash
# check-no-public-removals.sh — removal guard for the 2026-05-04-system-audit-refactor campaign.
#
# USAGE:
#   scripts/check-no-public-removals.sh [BASE_REF [HEAD_REF]]
#
#   BASE_REF  — git ref to diff from (default: pre-audit-refactor-2026-05-03)
#   HEAD_REF  — git ref to diff to   (default: HEAD)
#
# EXIT CODES:
#   0 — no protected public surface was removed (safe to proceed)
#   1 — one or more removals detected (prints offending lines; block the slice)
#
# HOW IT WORKS:
#   1. Fast grep-heuristic pass over the raw git diff — catches common patterns
#      in O(seconds) and gives early failures with file:line context.
#   2. Snapshot-diff pass — compares a freshly generated API snapshot (Go public
#      symbols + REST routes) against the BASE snapshot committed in the repo.
#      This is the authoritative gate; the grep pass is the fast pre-check.
#
# RULES CHECKED (per the hard rule in 2026-05-04-system-audit-refactor.md):
#   R1  Exported Go top-level decl deleted            internal/ cmd/ cli/
#   R2  Exported Go method deleted                    internal/ cmd/ cli/
#   R3  Go struct field with json/mapstructure/yaml/toml tag deleted
#   R4  Public TypeScript export deleted              web/src/components/ web/src/lib/
#   R5  Cobra / CLI flag registration deleted         anywhere
#   R6  REST endpoint registration deleted            internal/api/rest/router.go
#   R7  MCP tool registration deleted                 internal/api/rest/mcp_*.go
#   R8  Environment-variable read deleted             SOURCEBRIDGE_* CODEAWARE_*
#   R9  GraphQL schema line deleted                   internal/api/graphql/schema.graphqls
#   R10 FeatureFlags struct field deleted             internal/featureflags/flags.go
#
# SNAPSHOT DIFF (authoritative, Go public API + REST routes):
#   Snapshots live in thoughts/shared/plans/api-snapshot/<label>/.
#   Run scripts/snapshot-public-api.sh <label> to generate a snapshot before
#   making changes, then re-run this script to compare.

set -euo pipefail

BASE_REF="${1:-pre-audit-refactor-2026-05-03}"
HEAD_REF="${2:-HEAD}"

# Resolve the repo root from the caller's CWD so the script works both when
# invoked from the real repo and when invoked from a test fixture repo.
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$REPO_ROOT" ]]; then
  # Fallback: derive from script location (real-repo invocation from any dir)
  REPO_ROOT="$(git -C "$(dirname "$(realpath "$0")")" rev-parse --show-toplevel)"
fi
cd "$REPO_ROOT"

FAIL=0
ISSUES=()

# ─── helpers ─────────────────────────────────────────────────────────────────

red()    { printf '\033[0;31m%s\033[0m\n' "$*"; }
yellow() { printf '\033[0;33m%s\033[0m\n' "$*"; }
green()  { printf '\033[0;32m%s\033[0m\n' "$*"; }

fail() {
  local rule="$1"; shift
  local match="$1"; shift
  FAIL=1
  ISSUES+=("[$rule] $match")
  red "  VIOLATION [$rule]: $match"
}

section() { echo; yellow "==> $*"; }

# ─── produce the unified diff ─────────────────────────────────────────────────
# Using --diff-filter=M to include modifications (where lines are deleted from
# modified files) and --diff-filter=D for fully deleted files.
DIFF=$(git diff "$BASE_REF" "$HEAD_REF" -- \
  'internal/' 'cmd/' 'cli/' \
  'web/src/components/' 'web/src/lib/' \
  'internal/api/graphql/schema.graphqls' \
  2>/dev/null || true)

if [[ -z "$DIFF" ]]; then
  green "No diff between $BASE_REF and $HEAD_REF in watched paths — nothing to check."
  exit 0
fi

# ─── R1: Exported Go top-level decl deleted ──────────────────────────────────
section "R1: Exported Go top-level decl deleted (func/type/var/const [A-Z]...)"

while IFS= read -r line; do
  # Skip context lines and additions; only look at deletions
  [[ "$line" =~ ^-[^-] ]] || continue
  # Only Go files in guarded paths
  if echo "$DIFF" | grep -q "^diff --git a/\(internal\|cmd\|cli\).*\.go"; then
    if echo "$line" | grep -qE '^-[[:space:]]*(func|type|var|const)[[:space:]]+[A-Z]'; then
      fail "R1" "$line"
    fi
  fi
done < <(echo "$DIFF")

# Faster, file-aware pass using git diff --name-only + grep on per-file diffs
while IFS= read -r gofile; do
  [[ "$gofile" =~ \.(go)$ ]] || continue
  [[ "$gofile" =~ ^(internal|cmd|cli)/ ]] || continue
  fillediff=$(git diff "$BASE_REF" "$HEAD_REF" -- "$gofile" 2>/dev/null || true)
  [[ -z "$fillediff" ]] && continue
  while IFS= read -r match; do
    fail "R1" "$gofile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]*(func|type|var|const)[[:space:]]+[A-Z]' || true)
done < <(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null | grep -E '^(internal|cmd|cli)/.*\.go$' || true)

# ─── R2: Exported Go method deleted ──────────────────────────────────────────
section "R2: Exported Go method deleted (func (recv) [A-Z]...)"

while IFS= read -r gofile; do
  [[ "$gofile" =~ \.(go)$ ]] || continue
  [[ "$gofile" =~ ^(internal|cmd|cli)/ ]] || continue
  fillediff=$(git diff "$BASE_REF" "$HEAD_REF" -- "$gofile" 2>/dev/null || true)
  [[ -z "$fillediff" ]] && continue
  while IFS= read -r match; do
    fail "R2" "$gofile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]*func[[:space:]]*\([^)]+\)[[:space:]]+[A-Z]' || true)
done < <(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null | grep -E '^(internal|cmd|cli)/.*\.go$' || true)

# ─── R3: Go struct field with serialization tag deleted ───────────────────────
section "R3: Go struct field with json/mapstructure/yaml/toml tag deleted"

while IFS= read -r gofile; do
  [[ "$gofile" =~ \.(go)$ ]] || continue
  [[ "$gofile" =~ ^(internal|cmd|cli)/ ]] || continue
  fillediff=$(git diff "$BASE_REF" "$HEAD_REF" -- "$gofile" 2>/dev/null || true)
  [[ -z "$fillediff" ]] && continue
  while IFS= read -r match; do
    fail "R3" "$gofile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]+[A-Za-z][A-Za-z0-9_]*[[:space:]].*`[^`]*(json|mapstructure|yaml|toml):' || true)
done < <(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null | grep -E '^(internal|cmd|cli)/.*\.go$' || true)

# ─── R4: Public TypeScript export deleted ────────────────────────────────────
section "R4: Public TypeScript export deleted (web/src/components/ web/src/lib/)"

while IFS= read -r tsfile; do
  [[ "$tsfile" =~ \.(ts|tsx)$ ]] || continue
  [[ "$tsfile" =~ ^web/src/(components|lib)/ ]] || continue
  fillediff=$(git diff "$BASE_REF" "$HEAD_REF" -- "$tsfile" 2>/dev/null || true)
  [[ -z "$fillediff" ]] && continue
  # Named capital exports: export function Foo / export const Foo / export class Foo / export type Foo / export interface Foo
  while IFS= read -r match; do
    fail "R4" "$tsfile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]*export[[:space:]]+(function|const|class|type|interface|default function|default class)[[:space:]]+[A-Z]' || true)
  # export { Foo, Bar } — brace-list exports where at least one name starts with uppercase
  while IFS= read -r match; do
    fail "R4" "$tsfile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]*export[[:space:]]*\{' | grep -E '[A-Z][A-Za-z0-9_]*' || true)
done < <(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null | grep -E '^web/src/(components|lib)/.*\.(ts|tsx)$' || true)

# ─── R5: Cobra / CLI flag registration deleted ───────────────────────────────
section "R5: Cobra/CLI flag registration deleted"

while IFS= read -r gofile; do
  [[ "$gofile" =~ \.(go)$ ]] || continue
  fillediff=$(git diff "$BASE_REF" "$HEAD_REF" -- "$gofile" 2>/dev/null || true)
  [[ -z "$fillediff" ]] && continue
  # Individual flag bindings: .StringVar( .BoolVar( .IntVar( etc.
  while IFS= read -r match; do
    fail "R5" "$gofile: $match"
  done < <(echo "$fillediff" | grep -E '^-.*\.(StringVar|BoolVar|IntVar|Int64Var|Float64Var|DurationVar|StringSliceVar|StringP|BoolP|IntP)P?\(' || true)
  # RegisterFlags method signature deletion
  while IFS= read -r match; do
    fail "R5" "$gofile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]*func[[:space:]]*\([[:space:]]*[^)]+[[:space:]]+\*?[A-Za-z]+Flags\)[[:space:]]+RegisterFlags\b' || true)
done < <(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null | grep -E '\.go$' || true)

# ─── R6: REST endpoint registration deleted ───────────────────────────────────
section "R6: REST endpoint registration deleted (internal/api/rest/router.go)"

routerdiff=$(git diff "$BASE_REF" "$HEAD_REF" -- "internal/api/rest/router.go" 2>/dev/null || true)
if [[ -n "$routerdiff" ]]; then
  while IFS= read -r match; do
    fail "R6" "internal/api/rest/router.go: $match"
  done < <(echo "$routerdiff" | grep -E '^-[[:space:]]*r\.(Get|Post|Put|Delete|Patch|Head|Options|Handle)\(' || true)
fi

# ─── R7: MCP tool registration deleted ───────────────────────────────────────
section "R7: MCP tool registration deleted (h.registerTool)"

while IFS= read -r gofile; do
  [[ "$gofile" =~ ^internal/api/rest/mcp.*\.go$ ]] || continue
  fillediff=$(git diff "$BASE_REF" "$HEAD_REF" -- "$gofile" 2>/dev/null || true)
  [[ -z "$fillediff" ]] && continue
  while IFS= read -r match; do
    fail "R7" "$gofile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]*h\.registerTool\(' || true)
done < <(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null | grep -E '^internal/api/rest/mcp.*\.go$' || true)

# ─── R8: Environment-variable read deleted ────────────────────────────────────
section "R8: Env-var read deleted (SOURCEBRIDGE_* / CODEAWARE_*)"

while IFS= read -r gofile; do
  [[ "$gofile" =~ \.go$ ]] || continue
  fillediff=$(git diff "$BASE_REF" "$HEAD_REF" -- "$gofile" 2>/dev/null || true)
  [[ -z "$fillediff" ]] && continue
  while IFS= read -r match; do
    fail "R8" "$gofile: $match"
  done < <(echo "$fillediff" | grep -E '^-[[:space:]]*[a-zA-Z_]+[[:space:]]*:?=[[:space:]]*os\.Getenv\("(SOURCEBRIDGE|CODEAWARE)_' || true)
done < <(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null | grep -E '\.go$' || true)

# ─── R9: GraphQL schema line deleted ─────────────────────────────────────────
section "R9: GraphQL schema line deleted"

schemadiff=$(git diff "$BASE_REF" "$HEAD_REF" -- "internal/api/graphql/schema.graphqls" 2>/dev/null || true)
if [[ -n "$schemadiff" ]]; then
  # Any line that starts with - (not ---) inside the schema is potentially a removal
  # Ignore blank lines and comment lines being removed; flag field/type/query/mutation removals
  while IFS= read -r match; do
    # Filter out trivial comment and blank-line removals
    stripped="${match#-}"
    stripped="${stripped#"${stripped%%[![:space:]]*}"}"  # ltrim
    if [[ "$stripped" =~ ^# ]] || [[ -z "$stripped" ]]; then
      continue
    fi
    fail "R9" "schema.graphqls: $match"
  done < <(echo "$schemadiff" | grep -E '^-[^-]' || true)
fi

# ─── R10: FeatureFlags struct field deleted ───────────────────────────────────
section "R10: FeatureFlags struct field deleted (internal/featureflags/flags.go)"

flagsdiff=$(git diff "$BASE_REF" "$HEAD_REF" -- "internal/featureflags/flags.go" 2>/dev/null || true)
if [[ -n "$flagsdiff" ]]; then
  while IFS= read -r match; do
    fail "R10" "internal/featureflags/flags.go: $match"
  done < <(echo "$flagsdiff" | grep -E '^-[[:space:]]+[A-Z][A-Za-z0-9_]*[[:space:]]+(bool|string|int|int64|float64)\b' || true)
fi

# ─── SNAPSHOT-DIFF (authoritative gate) ──────────────────────────────────────
section "SNAPSHOT-DIFF: Go public API + REST routes"

SNAPSHOT_BASE_DIR="$REPO_ROOT/thoughts/shared/plans/api-snapshot"
BASE_LABEL="${BASE_REF//\//-}"  # replace slashes for dir name

if [[ ! -d "$SNAPSHOT_BASE_DIR/$BASE_LABEL" ]]; then
  yellow "  NOTE: No snapshot found at $SNAPSHOT_BASE_DIR/$BASE_LABEL"
  yellow "        Run: scripts/snapshot-public-api.sh $BASE_LABEL"
  yellow "        Snapshot-diff skipped; grep-heuristic rules above are the active gate."
else
  # Generate a HEAD snapshot in a temp dir and diff against base
  TMPSNAP=$(mktemp -d)
  trap 'rm -rf "$TMPSNAP"' EXIT

  # Go public API snapshot at HEAD
  (
    cd "$REPO_ROOT"
    go doc -all ./internal/... ./cmd/... ./cli/... 2>/dev/null \
      | grep -E '^(func|type|var|const|    [A-Z])' \
      | sort > "$TMPSNAP/go-public.txt"
  ) || true

  # REST routes snapshot at HEAD
  grep -h -E 'r\.(Get|Post|Put|Delete|Patch|Head|Options|Handle)\(' \
    "$REPO_ROOT/internal/api/rest/router.go" 2>/dev/null \
    | sed 's/^[[:space:]]*//' | sort > "$TMPSNAP/rest-routes.txt" || true

  snapshot_fail=0

  # Diff Go public API
  if [[ -f "$SNAPSHOT_BASE_DIR/$BASE_LABEL/go-public.txt" ]]; then
    removed_go=$(diff "$SNAPSHOT_BASE_DIR/$BASE_LABEL/go-public.txt" "$TMPSNAP/go-public.txt" \
      | grep -E '^<' || true)
    if [[ -n "$removed_go" ]]; then
      FAIL=1
      snapshot_fail=1
      red "  SNAPSHOT VIOLATION: Go public symbols removed relative to $BASE_LABEL:"
      while IFS= read -r line; do
        red "    $line"
        ISSUES+=("[SNAPSHOT-GO] $line")
      done <<< "$removed_go"
    fi
  else
    yellow "  No go-public.txt snapshot at $SNAPSHOT_BASE_DIR/$BASE_LABEL — skipping Go snapshot diff"
  fi

  # Diff REST routes
  if [[ -f "$SNAPSHOT_BASE_DIR/$BASE_LABEL/rest-routes.txt" ]]; then
    removed_rest=$(diff "$SNAPSHOT_BASE_DIR/$BASE_LABEL/rest-routes.txt" "$TMPSNAP/rest-routes.txt" \
      | grep -E '^<' || true)
    if [[ -n "$removed_rest" ]]; then
      FAIL=1
      snapshot_fail=1
      red "  SNAPSHOT VIOLATION: REST routes removed relative to $BASE_LABEL:"
      while IFS= read -r line; do
        red "    $line"
        ISSUES+=("[SNAPSHOT-REST] $line")
      done <<< "$removed_rest"
    fi
  else
    yellow "  No rest-routes.txt snapshot at $SNAPSHOT_BASE_DIR/$BASE_LABEL — skipping REST snapshot diff"
  fi

  [[ $snapshot_fail -eq 0 ]] && green "  Snapshot diff: no removals detected"
fi

# ─── SUMMARY ─────────────────────────────────────────────────────────────────
echo
if [[ $FAIL -ne 0 ]]; then
  red "======================================================================"
  red "REMOVAL-GUARD FAILED — ${#ISSUES[@]} violation(s) detected"
  red "======================================================================"
  for issue in "${ISSUES[@]}"; do
    red "  $issue"
  done
  echo
  red "Resolve these before landing this slice. Per the hard rule:"
  red "  'do them all autonomously but don't remove or reduce functionality.'"
  exit 1
else
  green "======================================================================"
  green "REMOVAL-GUARD PASSED — no protected public surface removed"
  green "  BASE: $BASE_REF"
  green "  HEAD: $HEAD_REF"
  green "======================================================================"
  exit 0
fi
