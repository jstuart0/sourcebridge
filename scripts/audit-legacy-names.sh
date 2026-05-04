#!/usr/bin/env bash
# audit-legacy-names.sh — census of CODEAWARE_* / codeaware references.
#
# USAGE:
#   scripts/audit-legacy-names.sh [--format=markdown|tsv|plain] [SEARCH_ROOT]
#
#   --format  Output format (default: markdown)
#   ROOT      Directory to search (default: repo root)
#
# OUTPUT:
#   Prints a markdown table (or TSV/plain) of every remaining CODEAWARE_* and
#   codeaware reference with file:line and a short context snippet.
#   Redirect to a file for persistence:
#       scripts/audit-legacy-names.sh > /tmp/codeaware-census.md
#
# PURPOSE (NAME-2 in Phase 4):
#   This census drives the CODEAWARE→SOURCEBRIDGE rename campaign.
#   References in this list fall into four buckets:
#     KEEP  — env vars still read by deployed infra (rename is a breaking change)
#     DEFER — DB table names, config keys, k8s resource names (out of scope)
#     RENAME — internal Go variable names, CSS class names, comment text
#     DONE  — already renamed (shows up here only if old name still present)
#
# FILE TYPES SEARCHED:
#   *.go, *.py, *.ts, *.tsx, *.yaml, *.yml, *.sh, *.md
#   Excludes: vendor/, node_modules/, gen/, .git/, *.pb.go, *_generated.go
#
# EXIT CODE: always 0 (census tool, not a gate)

set -euo pipefail

FORMAT="markdown"
REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
SEARCH_ROOT="$REPO_ROOT"

# Parse args
for arg in "$@"; do
  case "$arg" in
    --format=*)  FORMAT="${arg#--format=}" ;;
    --format)    echo "error: use --format=VALUE" >&2; exit 1 ;;
    *)           SEARCH_ROOT="$arg" ;;
  esac
done

cd "$REPO_ROOT"

GENERATED_AT="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
GIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"

# ─── collect matches ──────────────────────────────────────────────────────────

# Build exclude prune list for find
EXCLUDES=(
  -path '*/vendor/*' -prune -o
  -path '*/node_modules/*' -prune -o
  -path '*/gen/*' -prune -o
  -path '*/.git/*' -prune -o
  -path '*/repo-cache/*' -prune -o
)

# Collect hits: written to a temp file to avoid bash 3 mapfile dependency.
# grep exits 1 when no matches; we suppress that with || true to keep pipefail
# from aborting the loop. The per-file approach avoids nested pipeline issues.
HITSFILE="$(mktemp)"
FILELIST="$(mktemp)"
trap 'rm -f "$HITSFILE" "$FILELIST"' EXIT

# Build file list first (find itself won't exit non-zero for no results)
find "$SEARCH_ROOT" \
  "${EXCLUDES[@]}" \
  -type f \( \
    -name '*.go' \
    -o -name '*.py' \
    -o -name '*.ts' \
    -o -name '*.tsx' \
    -o -name '*.yaml' \
    -o -name '*.yml' \
    -o -name '*.sh' \
    -o -name '*.md' \
  \) -print 2>/dev/null \
  | grep -v '\.pb\.go$' \
  | grep -v '_generated\.go$' \
  | sort > "$FILELIST" || true

# Search each file; grep returns 1 on no match — use || true to continue
while IFS= read -r f; do
  [[ -f "$f" ]] || continue
  rel="${f#"$REPO_ROOT/"}"
  # Use xargs-style: capture output, don't let grep's exit code abort the loop
  grepout="$(grep -n -i "CODEAWARE\|codeaware" "$f" 2>/dev/null)" || true
  [[ -z "$grepout" ]] && continue
  while IFS= read -r gline; do
    lineno="${gline%%:*}"
    context="${gline#*:}"
    # Trim leading whitespace from context
    context="${context#"${context%%[![:space:]]*}"}"
    # Limit context length
    if [[ ${#context} -gt 120 ]]; then
      context="${context:0:117}..."
    fi
    printf '%s:%s:%s\n' "$rel" "$lineno" "$context"
  done <<< "$grepout"
done < "$FILELIST" > "$HITSFILE"

TOTAL="$(wc -l < "$HITSFILE" | tr -d ' ')"

# ─── output ───────────────────────────────────────────────────────────────────

case "$FORMAT" in
  tsv)
    printf 'file\tline\tcontext\n'
    while IFS= read -r hit; do
      file="${hit%%:*}"
      rest="${hit#*:}"
      lineno="${rest%%:*}"
      context="${rest#*:}"
      printf '%s\t%s\t%s\n' "$file" "$lineno" "$context"
    done < "$HITSFILE"
    ;;

  plain)
    echo "# CODEAWARE legacy-name census — $GENERATED_AT (sha: $GIT_SHA)"
    echo "# Total: $TOTAL references"
    echo
    cat "$HITSFILE"
    ;;

  markdown|*)
    cat <<HEADER
# CODEAWARE Legacy-Name Census

**Generated**: $GENERATED_AT
**Commit**: \`$GIT_SHA\`
**Total references**: $TOTAL
**Search root**: \`${SEARCH_ROOT#"$REPO_ROOT/"}\`

This census is input for Phase 4 Slice NAME-2 of the system-audit-refactor
campaign. Each reference should be triaged as one of:

- **KEEP** — still consumed by deployed infra (renaming would break prod)
- **DEFER** — DB table name, persisted config key, k8s resource name
- **RENAME** — safe to rename (internal var, CSS class, comment)

---

| File | Line | Context |
|------|------|---------|
HEADER

    if [[ "$TOTAL" -eq 0 ]]; then
      echo "| _(none found)_ | — | — |"
    else
      while IFS= read -r hit; do
        # Split: first colon is file separator, second is line number
        file="${hit%%:*}"
        rest="${hit#*:}"
        lineno="${rest%%:*}"
        context="${rest#*:}"
        # Escape pipe characters in context for markdown table
        context="${context//|/\\|}"
        # Escape backticks
        context="${context//\`/\\\`}"
        printf '| \`%s\` | %s | \`%s\` |\n' "$file" "$lineno" "$context"
      done < "$HITSFILE"
    fi

    cat <<FOOTER

---

## Notes

- \`CODEAWARE_*\` environment variables **must not** be removed; they are
  still read as fallbacks by deployed infrastructure (see CLAUDE.md §Legacy Name).
- Database table names (\`codeaware_*\`) are out of scope for this campaign.
- Internal Go variable names, CSS class names, and comment text are eligible
  for rename when the same commit removes all callers.

_Generated by \`scripts/audit-legacy-names.sh\` — re-run at any time for a fresh count._
FOOTER
    ;;
esac

exit 0
