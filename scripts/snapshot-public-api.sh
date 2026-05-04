#!/usr/bin/env bash
# snapshot-public-api.sh — generate API surface snapshots for diff-based removal detection.
#
# USAGE:
#   scripts/snapshot-public-api.sh LABEL
#
#   LABEL  — a short identifier for this snapshot (e.g. pre-phase-0, post-phase-2)
#            Snapshots are written to:
#            thoughts/shared/plans/api-snapshot/<LABEL>/
#
# OUTPUT FILES (all sorted, one entry per line):
#   go-public.txt    — exported Go symbols (funcs, types, vars, consts, methods,
#                      struct fields with serialization tags) across internal/ cmd/ cli/
#   rest-routes.txt  — r.{Get,Post,Put,Delete,Patch,Head,Options,Handle}(...) routes
#                      extracted from internal/api/rest/router.go
#   mcp-tools.txt    — MCP tool names from internal/api/rest/mcp_*.go (supports both
#                      pre-Phase-3 h.registerTool("name",...) and post-Phase-3
#                      mcpTool{Definition: defByName["name"],...} registration patterns)
#   ts-exports.txt   — public TypeScript exports (capital-initial names) from
#                      web/src/components/ and web/src/lib/
#
# PURPOSE:
#   Run BEFORE a phase to capture the baseline, run AFTER to verify nothing was removed.
#   check-no-public-removals.sh reads these snapshots for its authoritative diff gate.
#
# NOTE:
#   thoughts/ is gitignored — snapshots are local-only by design. They are not committed.
#   Generate fresh snapshots on each machine before starting a phase.
#
# DEPENDENCIES:
#   go (for go doc), grep, sed, awk — all standard on the dev machine.
#
# EXIT CODE:
#   0  — all snapshots written successfully
#   1  — fatal error (e.g. label not provided, go build fails)

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
cd "$REPO_ROOT"

LABEL="${1:-}"
if [[ -z "$LABEL" ]]; then
  echo "error: LABEL argument is required" >&2
  echo "usage: scripts/snapshot-public-api.sh <label>" >&2
  echo "       e.g. scripts/snapshot-public-api.sh pre-phase-0" >&2
  exit 1
fi

OUTDIR="$REPO_ROOT/thoughts/shared/plans/api-snapshot/$LABEL"
mkdir -p "$OUTDIR"

GIT_SHA="$(git rev-parse HEAD 2>/dev/null || echo 'unknown')"
GIT_REF="$(git describe --tags --exact-match 2>/dev/null || git rev-parse --abbrev-ref HEAD 2>/dev/null || echo 'detached')"

echo "Generating API snapshots for label: $LABEL"
echo "  Commit: $GIT_SHA ($GIT_REF)"
echo "  Output: $OUTDIR"
echo

# ─── go-public.txt ───────────────────────────────────────────────────────────
echo "  [1/4] Go public API (internal/ cmd/ cli/)..."

{
  # go doc -all for each package; filter to exported top-level lines
  for dir in internal cmd cli; do
    [[ -d "$dir" ]] || continue
    # go list returns one package path per line
    go list "./$dir/..." 2>/dev/null | while read -r pkg; do
      go doc -all "$pkg" 2>/dev/null || true
    done
  done
} | grep -E '^(func |type |var |const |    [A-Z][A-Za-z0-9_]* )' \
  | sed 's/[[:space:]]*$//' \
  | sort -u \
  > "$OUTDIR/go-public.txt"

GO_COUNT=$(wc -l < "$OUTDIR/go-public.txt" | tr -d ' ')
echo "     -> $GO_COUNT exported symbols"

# ─── rest-routes.txt ─────────────────────────────────────────────────────────
echo "  [2/4] REST routes (internal/api/rest/router.go)..."

ROUTER="$REPO_ROOT/internal/api/rest/router.go"
if [[ -f "$ROUTER" ]]; then
  grep -E 'r\.(Get|Post|Put|Delete|Patch|Head|Options|Handle)\(' "$ROUTER" \
    | sed 's/^[[:space:]]*//' \
    | sort -u \
    > "$OUTDIR/rest-routes.txt"
  ROUTE_COUNT=$(wc -l < "$OUTDIR/rest-routes.txt" | tr -d ' ')
  echo "     -> $ROUTE_COUNT route registrations"
else
  echo "     -> WARNING: router.go not found at $ROUTER" >&2
  touch "$OUTDIR/rest-routes.txt"
fi

# ─── mcp-tools.txt ───────────────────────────────────────────────────────────
echo "  [3/4] MCP tools (internal/api/rest/mcp_*.go)..."

MCP_DIR="$REPO_ROOT/internal/api/rest"
# Include mcp.go (coreTools lives there) alongside mcp_*.go files.
MCP_FILES=( "$MCP_DIR"/mcp.go "$MCP_DIR"/mcp_*.go )
if [[ -f "$MCP_DIR/mcp.go" ]] || ls "$MCP_DIR"/mcp_*.go >/dev/null 2>&1; then
  {
    # Pattern 1 (pre-Phase-3): h.registerTool("tool_name", ...)
    grep -h 'h\.registerTool(' "${MCP_FILES[@]}" \
      | grep -oE '"[a-z_]+"' \
      | tr -d '"' \
      || true
    # Pattern 2 (post-Phase-3 Slice 1): Definition: defByName["tool_name"]
    grep -h 'Definition:[[:space:]]*defByName\[' "${MCP_FILES[@]}" \
      | grep -oE '"[a-z_]+"' \
      | tr -d '"' \
      || true
    # Pattern 3 (record_change special case): Definition: recordChangeToolDef()
    # Extract the Name field from recordChangeToolDef() return value
    grep -h 'Name:.*"record_change"' "${MCP_FILES[@]}" \
      | grep -oE '"[a-z_]+"' \
      | tr -d '"' \
      || true
  } | sort -u > "$OUTDIR/mcp-tools.txt"
  MCP_COUNT=$(wc -l < "$OUTDIR/mcp-tools.txt" | tr -d ' ')
  echo "     -> $MCP_COUNT MCP tools registered"
else
  echo "     -> WARNING: no MCP source files found in $MCP_DIR" >&2
  touch "$OUTDIR/mcp-tools.txt"
fi

# ─── ts-exports.txt ──────────────────────────────────────────────────────────
echo "  [4/4] TypeScript exports (web/src/components/ web/src/lib/)..."

{
  for tsdir in web/src/components web/src/lib; do
    [[ -d "$tsdir" ]] || continue
    find "$tsdir" -type f \( -name '*.ts' -o -name '*.tsx' \) \
      | sort \
      | while read -r tsfile; do
          # Named exports with capital-initial name
          grep -nE '^export[[:space:]]+(function|const|class|type|interface|default function|default class)[[:space:]]+[A-Z]' \
            "$tsfile" 2>/dev/null \
            | sed "s|^|${tsfile#"$REPO_ROOT/"}:|" \
            || true
          # Brace-list exports: export { Foo, Bar }
          grep -nE '^export[[:space:]]*\{[^}]*[A-Z][A-Za-z0-9_]*' \
            "$tsfile" 2>/dev/null \
            | sed "s|^|${tsfile#"$REPO_ROOT/"}:|" \
            || true
        done
  done
} | sort -u > "$OUTDIR/ts-exports.txt"

TS_COUNT=$(wc -l < "$OUTDIR/ts-exports.txt" | tr -d ' ')
echo "     -> $TS_COUNT public TS export lines"

# ─── metadata ─────────────────────────────────────────────────────────────────
cat > "$OUTDIR/SNAPSHOT_META.txt" <<META
label:      $LABEL
commit:     $GIT_SHA
ref:        $GIT_REF
generated:  $(date -u '+%Y-%m-%dT%H:%M:%SZ')
go-public:  $GO_COUNT symbols
rest-routes: $ROUTE_COUNT routes
mcp-tools:  $MCP_COUNT tools
ts-exports: $TS_COUNT export lines
META

echo
echo "Done. Snapshot written to: $OUTDIR"
echo
echo "To verify no removals after making changes:"
echo "  scripts/check-no-public-removals.sh <BASE_REF> HEAD"
echo "  (The script will auto-discover this snapshot at $OUTDIR)"

exit 0
