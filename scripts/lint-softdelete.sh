#!/usr/bin/env bash
# Soft-delete read-path linter.
#
# Fails if any SELECT / UPDATE against a trashable SurrealDB table is
# missing the `deleted_at IS NONE` filter. Called from CI to prevent
# regressions as new read paths are added.
#
# Adding a new trashable table? Add it to TABLES below *and* update
# migration 031 (and later: a follow-up migration) to carry the
# soft-delete columns.
#
# Intentional exemptions:
#   * Queries inside internal/trash itself — the trash package is the
#     one legitimate reader of tombstoned rows.
#   * Unwind scripts under internal/db/migrations/ — they mutate
#     tombstoned rows explicitly.
#   * Schema migrations themselves (the DEFINE FIELD / DEFINE INDEX
#     statements are not "reads").

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

TABLES=(
  "ca_requirement"
  "ca_link"
  "ca_knowledge_artifact"
  "ca_knowledge_section"
  "ca_knowledge_evidence"
  "ca_knowledge_dependency"
  "ca_knowledge_refinement"
)

violations_file="$(mktemp)"
trap "rm -f $violations_file" EXIT

for table in "${TABLES[@]}"; do
  # Match both bare-table forms (SELECT * FROM ca_requirement WHERE …)
  # and quoted forms (UPDATE type::thing('ca_requirement', ...)). We
  # lookup SELECT/UPDATE on the same line that mentions the table
  # name adjacent to one of: whitespace, quote, backtick, paren, comma.
  grep -RIn --include='*.go' --include='*.py' \
    -E "(SELECT|UPDATE).*${table}[[:space:]\"'\`(),]" . 2>/dev/null \
    || true
done > "$violations_file.raw" || true

# Filter out exempt paths + queries that already have the soft-delete filter.
while IFS= read -r line; do
  [ -z "$line" ] && continue
  file="${line%%:*}"
  case "$file" in
    *internal/trash/*) continue ;;
    *internal/db/migrations/*) continue ;;
    *scripts/lint-softdelete.sh*) continue ;;
  esac
  # Soft-delete filter may be on the match line OR on any of the next
  # 10 lines of the same file (multi-line UPDATE queries are common).
  # Extract file + line number, read a 10-line window.
  file_part="${line%%:*}"
  rest="${line#*:}"
  line_no="${rest%%:*}"
  if [ -z "$file_part" ] || [ -z "$line_no" ]; then
    echo "$line" >> "$violations_file"
    continue
  fi
  end_line=$((line_no + 15))
  if sed -n "${line_no},${end_line}p" "$file_part" 2>/dev/null | grep -qE 'deleted_at[[:space:]]+IS[[:space:]]+NONE'; then
    continue
  fi
  echo "$line" >> "$violations_file"
done < "$violations_file.raw"

rm -f "$violations_file.raw"

if [ -s "$violations_file" ]; then
  count="$(wc -l < "$violations_file" | tr -d ' ')"
  echo "soft-delete lint FAILED: $count violation(s)."
  echo
  cat "$violations_file"
  echo
  echo "Every SELECT / UPDATE against a trashable table must filter"
  echo "'deleted_at IS NONE' (or live in internal/trash/, the one"
  echo "legitimate reader of tombstoned rows)."
  exit 1
fi

echo "soft-delete lint OK"
