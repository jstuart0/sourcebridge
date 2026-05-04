#!/usr/bin/env bash
# test-removal-guard.sh — verifies that check-no-public-removals.sh catches every
# protected removal class. Runs 8 deliberate-break fixtures and asserts each one
# produces a non-zero exit.
#
# USAGE:
#   scripts/test-removal-guard.sh [--verbose]
#
#   --verbose   Print the guard output for each scenario (default: suppressed)
#
# EXIT CODES:
#   0  — all 8 scenarios correctly detected (guard is healthy)
#   1  — one or more scenarios were NOT detected (guard has a false negative)
#
# SCENARIOS TESTED:
#   a) Deleted exported Go function (top-level)
#   b) Deleted exported Go type (top-level)
#   c) Deleted exported Go method (receiver form)
#   d) Deleted Go struct field with json: tag
#   e) Deleted Go struct field with mapstructure: tag
#   f) Deleted REST route registration (router.go pattern)
#   g) Deleted public TypeScript export (web/src/components/ path)
#   h) Deleted Cobra flag registration (StringVar / BoolVar)
#
# HOW IT WORKS:
#   For each scenario this script:
#   1. Creates a temporary git repository with a minimal fixture file.
#   2. Commits the "before" state (the line to be protected exists).
#   3. Removes the protected line and commits the "after" state.
#   4. Invokes check-no-public-removals.sh against that commit pair.
#   5. Asserts the script exits non-zero.
#   Cleanup: the temp repo is removed after each scenario (and on EXIT).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GUARD="$SCRIPT_DIR/check-no-public-removals.sh"
VERBOSE=0

for arg in "$@"; do
  [[ "$arg" == "--verbose" ]] && VERBOSE=1
done

if [[ ! -x "$GUARD" ]]; then
  echo "error: $GUARD not found or not executable" >&2
  exit 1
fi

# ─── helpers ──────────────────────────────────────────────────────────────────

PASS=0
FAIL=0

red()    { printf '\033[0;31m%s\033[0m\n' "$*"; }
green()  { printf '\033[0;32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[0;33m%s\033[0m\n' "$*"; }

# run_scenario NAME FIXTURE_PATH FIXTURE_CONTENT REMOVE_PATTERN
#   FIXTURE_PATH    — relative path inside fixture repo (e.g. internal/foo.go)
#   FIXTURE_CONTENT — full file content for the "before" commit
#   REMOVE_PATTERN  — exact string line to remove for the "after" commit
run_scenario() {
  local name="$1"
  local fixture_path="$2"
  local fixture_content="$3"
  local remove_line="$4"

  local tmpdir
  tmpdir="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmpdir'" RETURN

  # Init a bare-minimum git repo
  git -C "$tmpdir" init -q
  git -C "$tmpdir" config user.email "test@test.invalid"
  git -C "$tmpdir" config user.name "Test"

  # Create fixture file directory structure
  mkdir -p "$tmpdir/$(dirname "$fixture_path")"
  printf '%s\n' "$fixture_content" > "$tmpdir/$fixture_path"

  # Commit the "before" state
  git -C "$tmpdir" add -A
  git -C "$tmpdir" commit -q -m "before: $name"

  # Tag it as the base ref
  git -C "$tmpdir" tag "base"

  # Remove the protected line
  # Use grep -v with fixed-string match to remove exactly one line
  local tmpfile
  tmpfile="$(mktemp)"
  grep -vF "$remove_line" "$tmpdir/$fixture_path" > "$tmpfile" || true
  mv "$tmpfile" "$tmpdir/$fixture_path"

  # Commit the "after" state (deliberate breakage)
  git -C "$tmpdir" add -A
  git -C "$tmpdir" commit -q -m "after: $name (deliberate removal)"

  # Run the guard against this fixture repo
  local guard_output
  local guard_exit=0
  guard_output=$(
    cd "$tmpdir"
    bash "$GUARD" "base" "HEAD" 2>&1
  ) || guard_exit=$?

  if [[ $VERBOSE -eq 1 ]]; then
    echo "  --- guard output ---"
    echo "$guard_output" | sed 's/^/    /'
    echo "  --- exit: $guard_exit ---"
  fi

  if [[ $guard_exit -ne 0 ]]; then
    green "  PASS [$name]: guard correctly detected removal (exit $guard_exit)"
    PASS=$((PASS + 1))
  else
    red  "  FAIL [$name]: guard DID NOT detect removal (exit 0 — false negative!)"
    if [[ $VERBOSE -eq 0 ]]; then
      echo "  Guard output:"
      echo "$guard_output" | sed 's/^/    /'
    fi
    FAIL=$((FAIL + 1))
  fi
}

# ─── scenario definitions ─────────────────────────────────────────────────────

echo
yellow "=== check-no-public-removals.sh deliberate-break test suite ==="
echo

# ── Scenario A: deleted exported Go function ──────────────────────────────────
echo "Scenario A: deleted exported Go function"
run_scenario "A-exported-func" \
  "internal/foo/foo.go" \
  'package foo

// ListRepos returns the list of repositories.
func ListRepos(ctx context.Context) ([]string, error) {
	return nil, nil
}

func internalHelper() {}
' \
  "func ListRepos(ctx context.Context) ([]string, error) {"

# ── Scenario B: deleted exported Go type ─────────────────────────────────────
echo "Scenario B: deleted exported Go type"
run_scenario "B-exported-type" \
  "internal/bar/types.go" \
  'package bar

// RepoConfig holds repository configuration.
type RepoConfig struct {
	ID   string
	Name string
}

type internalState struct{}
' \
  "type RepoConfig struct {"

# ── Scenario C: deleted exported Go method ────────────────────────────────────
echo "Scenario C: deleted exported Go method on a receiver"
run_scenario "C-exported-method" \
  "internal/arch/document.go" \
  'package arch

type DiagramDocument struct{}

// GenerateMermaid produces a Mermaid diagram string.
func (doc *DiagramDocument) GenerateMermaid() string {
	return ""
}

func (doc *DiagramDocument) internalRender() {}
' \
  "func (doc *DiagramDocument) GenerateMermaid() string {"

# ── Scenario D: deleted struct field with json: tag ───────────────────────────
echo "Scenario D: deleted Go struct field with json: tag"
run_scenario "D-json-tag-field" \
  "internal/auth/api_tokens.go" \
  'package auth

type APIToken struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}
' \
  '	Role      string `json:"role"`'

# ── Scenario E: deleted struct field with mapstructure: tag ───────────────────
echo "Scenario E: deleted Go struct field with mapstructure: tag"
run_scenario "E-mapstructure-tag-field" \
  "internal/config/config.go" \
  'package config

type Security struct {
	CSRFEnabled bool   `mapstructure:"csrf_enabled"`
	SecretKey   string `mapstructure:"secret_key"`
}
' \
  '	CSRFEnabled bool   `mapstructure:"csrf_enabled"`'

# ── Scenario F: deleted REST route registration ───────────────────────────────
echo "Scenario F: deleted REST route registration in router.go"
run_scenario "F-rest-route" \
  "internal/api/rest/router.go" \
  'package rest

func (s *Server) routes(r Router) {
	r.Get("/healthz", s.handleHealthz)
	r.Post("/api/v1/search", s.handleSearch)
	r.Get("/api/v1/repos", s.handleListRepos)
}
' \
  "	r.Post(\"/api/v1/search\", s.handleSearch)"

# ── Scenario G: deleted public TS export ─────────────────────────────────────
echo "Scenario G: deleted public TypeScript export in web/src/components/"
run_scenario "G-ts-export" \
  "web/src/components/ui/empty-state.tsx" \
  '"use client";
import React from "react";

export interface EmptyStateProps {
  title: string;
  description: string;
}

export function EmptyState({ title, description }: EmptyStateProps) {
  return <div><h3>{title}</h3><p>{description}</p></div>;
}

function internalHelper() {}
' \
  "export function EmptyState({ title, description }: EmptyStateProps) {"

# ── Scenario H: deleted Cobra flag registration ───────────────────────────────
echo "Scenario H: deleted Cobra flag registration (StringVar)"
run_scenario "H-cobra-flag" \
  "cli/index.go" \
  'package cli

import "github.com/spf13/cobra"

var indexJSON bool
var indexRetry bool

func init() {
	indexCmd.Flags().BoolVar(&indexJSON, "json", false, "Output results as JSON")
	indexCmd.Flags().BoolVar(&indexRetry, "retry", false, "Retry previously failed indexing")
}

var indexCmd = &cobra.Command{Use: "index"}
' \
  '	indexCmd.Flags().BoolVar(&indexRetry, "retry", false, "Retry previously failed indexing")'

# ─── SUMMARY ─────────────────────────────────────────────────────────────────
echo
yellow "=== Results ==="
echo "  Passed: $PASS / 8"
echo "  Failed: $FAIL / 8"
echo

if [[ $FAIL -eq 0 ]]; then
  green "ALL 8 SCENARIOS PASSED — removal guard is healthy."
  exit 0
else
  red "$FAIL SCENARIO(S) FAILED — guard has false negatives; fix before proceeding."
  exit 1
fi
