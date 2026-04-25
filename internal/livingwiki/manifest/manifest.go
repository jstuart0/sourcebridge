// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package manifest defines the typed dependency manifest embedded in every
// living-wiki page as YAML frontmatter (the `sourcebridge:` block).
//
// The manifest captures:
//   - Page identity and template/audience metadata.
//   - Code dependencies (paths, symbols, packages) that drive regeneration.
//   - Stale-detection conditions that surface banners without forcing regen.
//
// # Frontmatter layout
//
//	---
//	sourcebridge:
//	  page_id: arch.auth
//	  template: architecture
//	  audience: for-engineers
//	  dependencies:
//	    paths: [internal/auth/**]
//	    symbols: [auth.Middleware, auth.RequireRole]
//	    upstream_packages: [internal/api/rest]
//	    downstream_packages: [internal/jwt]
//	    dependency_scope: direct
//	  stale_when:
//	    - signature_change_in: [auth.Middleware]
//	    - new_caller_added_to: [auth.RequireRole]
//	---
//
// Call [ParseFrontmatter] to split an existing page and unmarshal the
// manifest. Call [WriteFrontmatter] to reassemble the page.
//
// To compute which pages are affected by a code change, call
// [AffectedPages]. To evaluate stale banners without a full regen, call
// [EvaluateStaleConditions].
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// DependencyScope controls how far the dependency graph is walked when
// deciding whether a page needs regeneration.
type DependencyScope string

const (
	// ScopeDirect limits regeneration to changes that directly touch the
	// paths or symbols listed in the manifest. This is the default.
	ScopeDirect DependencyScope = "direct"

	// ScopeTransitive also walks N levels deeper in the call graph. Used by
	// system-overview pages that want to surface subsystem-level changes.
	ScopeTransitive DependencyScope = "transitive"

	// ScopeRuntime includes runtime call paths from indexed traces.
	// Reserved for a future release that ingests runtime-trace data.
	ScopeRuntime DependencyScope = "runtime"
)

// Dependencies describes the code artifacts that a page depends on.
type Dependencies struct {
	// Paths is a list of glob patterns matching source files or directories
	// that this page documents. Supports "**" wildcard.
	Paths []string `yaml:"paths,omitempty"`

	// Symbols is a list of fully-qualified symbol names (e.g. "auth.Middleware")
	// that this page specifically documents or cites.
	Symbols []string `yaml:"symbols,omitempty"`

	// UpstreamPackages are packages that import this page's package.
	// A change in an upstream package can affect the documented interface.
	UpstreamPackages []string `yaml:"upstream_packages,omitempty"`

	// DownstreamPackages are packages that this page's package imports.
	// Signature changes in downstream packages can invalidate documented behavior.
	DownstreamPackages []string `yaml:"downstream_packages,omitempty"`

	// DependencyScope controls how broadly the dependency graph is walked
	// when deciding whether a push should trigger regeneration of this page.
	// Defaults to ScopeDirect when zero.
	DependencyScope DependencyScope `yaml:"dependency_scope,omitempty"`
}

// StaleCondition is one condition that, when true, causes the page to
// receive a stale-banner block without triggering a full regen.
//
// Exactly one of the condition fields must be non-nil per instance; this
// models a discriminated union within the YAML list.
type StaleCondition struct {
	// SignatureChangeIn lists symbols whose signature change triggers staleness.
	SignatureChangeIn []string `yaml:"signature_change_in,omitempty"`

	// NewCallerAddedTo lists symbols whose new-caller event triggers staleness.
	NewCallerAddedTo []string `yaml:"new_caller_added_to,omitempty"`
}

// DependencyManifest is the complete typed manifest for one living-wiki page.
// It is stored in YAML frontmatter under the "sourcebridge:" key.
type DependencyManifest struct {
	// PageID is the stable, human-readable identifier for the page.
	// Examples: "arch.auth", "api.rest.middleware", "overview.system".
	// Once assigned this value must never change, as it is used as the
	// primary key in overlay and AST storage.
	PageID string `yaml:"page_id"`

	// Template identifies which page template this page uses.
	// Must match one of the quality.Template constants.
	Template string `yaml:"template"`

	// Audience identifies the target audience for this page.
	// Must match one of the quality.Audience constants.
	Audience string `yaml:"audience"`

	// Dependencies describes the code artifacts this page tracks.
	Dependencies Dependencies `yaml:"dependencies,omitempty"`

	// StaleWhen is a list of conditions. When any condition evaluates to
	// true for a given diff, the page gets a stale-banner block injected
	// into its AST (rendered as visible content per A1.P7), but is not
	// fully regenerated unless it was already in the affected set.
	StaleWhen []StaleCondition `yaml:"stale_when,omitempty"`
}

// frontmatterEnvelope is the top-level YAML node that wraps the manifest.
// The manifest is nested under the "sourcebridge" key so it can coexist
// with other frontmatter keys from other tooling.
type frontmatterEnvelope struct {
	SourceBridge DependencyManifest `yaml:"sourcebridge"`
}

// frontmatterDelimiter is the standard YAML front-matter delimiter line.
const frontmatterDelimiter = "---"

// ParseFrontmatter splits a markdown page that begins with a YAML frontmatter
// block into its manifest and body.
//
// The frontmatter block must start at byte 0 and be delimited by "---" lines.
// Only the "sourcebridge:" key is read; other frontmatter keys are silently
// ignored. If the page has no frontmatter, it returns a zero-value manifest
// and the full src as body without error.
//
// Returns an error when the frontmatter is present but malformed.
func ParseFrontmatter(src []byte) (DependencyManifest, []byte, error) {
	if !bytes.HasPrefix(src, []byte(frontmatterDelimiter)) {
		return DependencyManifest{}, src, nil
	}

	// Find the closing delimiter after the first line.
	rest := src[len(frontmatterDelimiter):]
	// Skip the newline after the opening "---".
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 0 && rest[0] == '\r' && len(rest) > 1 && rest[1] == '\n' {
		rest = rest[2:]
	}

	closingIdx := findClosingDelimiter(rest)
	if closingIdx < 0 {
		return DependencyManifest{}, src, errors.New("manifest: frontmatter has no closing '---' delimiter")
	}

	yamlContent := rest[:closingIdx]
	body := rest[closingIdx+len(frontmatterDelimiter):]
	// Trim leading newline from body.
	body = bytes.TrimPrefix(body, []byte("\r\n"))
	body = bytes.TrimPrefix(body, []byte("\n"))

	var env frontmatterEnvelope
	if err := yaml.Unmarshal(yamlContent, &env); err != nil {
		return DependencyManifest{}, nil, fmt.Errorf("manifest: malformed frontmatter YAML: %w", err)
	}

	return env.SourceBridge, body, nil
}

// findClosingDelimiter finds the byte offset of the closing "---" delimiter
// within rest. Returns -1 if not found. The delimiter must appear at the
// start of a line.
func findClosingDelimiter(rest []byte) int {
	delim := []byte(frontmatterDelimiter)
	search := rest
	offset := 0

	for len(search) >= len(delim) {
		idx := bytes.Index(search, delim)
		if idx < 0 {
			return -1
		}
		// The delimiter must start at the beginning of a line.
		if idx == 0 || search[idx-1] == '\n' {
			return offset + idx
		}
		// Skip past this occurrence and keep searching.
		advance := idx + 1
		offset += advance
		search = search[advance:]
	}
	return -1
}

// WriteFrontmatter writes a frontmatter block followed by body to w.
// The manifest is serialized under the "sourcebridge:" key. If manifest
// is a zero value (empty PageID), no frontmatter is written.
func WriteFrontmatter(w io.Writer, m DependencyManifest, body []byte) error {
	if m.PageID == "" {
		_, err := w.Write(body)
		return err
	}

	env := frontmatterEnvelope{SourceBridge: m}
	yamlBytes, err := yaml.Marshal(env)
	if err != nil {
		return fmt.Errorf("manifest: failed to marshal frontmatter: %w", err)
	}

	if _, err := fmt.Fprintf(w, "%s\n", frontmatterDelimiter); err != nil {
		return err
	}
	if _, err := w.Write(yamlBytes); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s\n", frontmatterDelimiter); err != nil {
		return err
	}
	if len(body) > 0 {
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	return nil
}

// ChangedPair is a (path, symbol) pair representing one changed item in a diff.
// Path is the file path relative to the repo root; Symbol is the fully-qualified
// symbol name, or empty when the change is file-level only.
type ChangedPair struct {
	Path   string
	Symbol string
}

// PackageGraphResolver answers package-level graph-walk queries needed by
// [AffectedPages]. The implementation is supplied by the caller; this package
// has no direct dependency on the graph store.
//
// All methods must be safe for concurrent use.
type PackageGraphResolver interface {
	// PackageForPath returns the package import path that owns the given
	// file path, or ("", false) when unknown.
	PackageForPath(filePath string) (pkg string, ok bool)

	// TransitiveDependants returns all package import paths that directly or
	// transitively depend on pkg, up to maxDepth levels of the call graph.
	// maxDepth ≤ 0 means unbounded (walk the full graph).
	TransitiveDependants(pkg string, maxDepth int) []string
}

// AffectedPage records a page that is affected by a set of changes, together
// with the reason it was included.
type AffectedPage struct {
	Manifest DependencyManifest
	// DirectHit is true when a changed path or symbol matches this page's
	// dependencies.paths or dependencies.symbols directly.
	DirectHit bool
	// GraphHit is true when the changed package appears in the page's
	// upstream_packages or downstream_packages.
	GraphHit bool
	// TransitiveHit is true when the page has dependency_scope: transitive
	// and the changed package appeared within the graph walk.
	TransitiveHit bool
}

// AffectedPages returns the subset of pages whose manifests indicate they
// should be regenerated given the set of changed code pairs.
//
// The algorithm follows the plan's "Diff-to-affected-pages logic":
//  1. Direct hit: the changed (path, symbol) matches dependencies.paths or
//     dependencies.symbols.
//  2. Graph hit: the changed pair's package is listed in the page's
//     upstream_packages or downstream_packages.
//  3. Transitive hit (opt-in): for pages with dependency_scope=transitive,
//     also walk up to transitiveDepth levels of the call graph.
//
// transitiveDepth is the maximum number of hops for graph walks of pages
// with ScopeTransitive. Pass 2 to match the plan's default. Pass 0 or
// negative for unbounded (not recommended on large graphs).
//
// resolver may be nil when the caller knows there are no transitive pages
// (e.g. in tests); graph-hit detection based on explicitly listed packages
// still works without it — only the call-graph walk requires it.
func AffectedPages(changed []ChangedPair, pages []DependencyManifest, resolver PackageGraphResolver, transitiveDepth int) []AffectedPage {
	if len(changed) == 0 || len(pages) == 0 {
		return nil
	}

	// Build lookup sets from the changed pairs.
	changedPaths := make(map[string]bool, len(changed))
	changedSymbols := make(map[string]bool, len(changed))
	for _, cp := range changed {
		if cp.Path != "" {
			changedPaths[cp.Path] = true
		}
		if cp.Symbol != "" {
			changedSymbols[cp.Symbol] = true
		}
	}

	// Resolve changed packages once (needed for graph hits).
	changedPackages := resolvePackages(changed, resolver)

	// Compute transitive dependants per changed package for transitive pages.
	// We compute this lazily per (package, depth) pair.
	var transitiveCache map[string]map[string]bool // pkg → set of dependants

	getTransitiveDeps := func(pkg string) map[string]bool {
		if resolver == nil {
			return nil
		}
		if transitiveCache == nil {
			transitiveCache = make(map[string]map[string]bool)
		}
		if deps, ok := transitiveCache[pkg]; ok {
			return deps
		}
		deps := make(map[string]bool)
		for _, dep := range resolver.TransitiveDependants(pkg, transitiveDepth) {
			deps[dep] = true
		}
		transitiveCache[pkg] = deps
		return deps
	}

	var result []AffectedPage

	for _, m := range pages {
		var directHit, graphHit, transitiveHit bool

		scope := m.Dependencies.DependencyScope
		if scope == "" {
			scope = ScopeDirect
		}

		// --- Step 1: Direct hit on paths ---
		for _, pattern := range m.Dependencies.Paths {
			for path := range changedPaths {
				if matchGlob(pattern, path) {
					directHit = true
					break
				}
			}
			if directHit {
				break
			}
		}

		// --- Step 1 continued: Direct hit on symbols ---
		if !directHit {
			for _, sym := range m.Dependencies.Symbols {
				if changedSymbols[sym] {
					directHit = true
					break
				}
			}
		}

		// --- Step 2: Graph hit via explicitly listed packages ---
		if !directHit {
			for pkg := range changedPackages {
				if containsString(m.Dependencies.UpstreamPackages, pkg) ||
					containsString(m.Dependencies.DownstreamPackages, pkg) {
					graphHit = true
					break
				}
			}
		}

		// --- Step 3: Transitive hit (opt-in) ---
		if !directHit && !graphHit && scope == ScopeTransitive {
			for pkg := range changedPackages {
				deps := getTransitiveDeps(pkg)
				for _, up := range m.Dependencies.UpstreamPackages {
					if deps[up] {
						transitiveHit = true
						break
					}
				}
				if !transitiveHit {
					for _, down := range m.Dependencies.DownstreamPackages {
						if deps[down] {
							transitiveHit = true
							break
						}
					}
				}
				if transitiveHit {
					break
				}
			}
		}

		if directHit || graphHit || transitiveHit {
			result = append(result, AffectedPage{
				Manifest:      m,
				DirectHit:     directHit,
				GraphHit:      graphHit,
				TransitiveHit: transitiveHit,
			})
		}
	}

	return result
}

// resolvePackages extracts unique package paths from the changed pairs using
// the resolver. Pairs whose path cannot be resolved are silently skipped.
func resolvePackages(changed []ChangedPair, resolver PackageGraphResolver) map[string]bool {
	pkgs := make(map[string]bool)
	if resolver == nil {
		return pkgs
	}
	for _, cp := range changed {
		if cp.Path == "" {
			continue
		}
		if pkg, ok := resolver.PackageForPath(cp.Path); ok && pkg != "" {
			pkgs[pkg] = true
		}
	}
	return pkgs
}

// matchGlob matches a file path against a glob pattern. Supports "**" as a
// multi-segment wildcard. Path and pattern are slash-separated.
func matchGlob(pattern, path string) bool {
	// Normalise separators.
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	path = strings.ReplaceAll(path, "\\", "/")

	return globMatch(pattern, path)
}

// globMatch recursively matches pattern segments against path. "**" matches
// zero or more path segments.
func globMatch(pattern, path string) bool {
	if pattern == "" {
		return path == ""
	}
	if pattern == "**" {
		return true
	}

	pp := strings.SplitN(pattern, "/", 2)
	seg := pp[0]
	patternRest := ""
	if len(pp) > 1 {
		patternRest = pp[1]
	}

	pathParts := strings.SplitN(path, "/", 2)
	pathSeg := pathParts[0]
	pathRest := ""
	if len(pathParts) > 1 {
		pathRest = pathParts[1]
	}

	if seg == "**" {
		// "**" can consume zero segments: try skipping it.
		if globMatch(patternRest, path) {
			return true
		}
		// "**" can consume one segment: advance in path, keep "**" in pattern.
		if pathSeg != "" && globMatch(pattern, pathRest) {
			return true
		}
		return false
	}

	// Single-segment match.
	if !segmentMatch(seg, pathSeg) {
		return false
	}

	// Both must be exhausted together.
	if patternRest == "" && pathRest == "" {
		return true
	}
	if patternRest == "" || pathRest == "" {
		return false
	}
	return globMatch(patternRest, pathRest)
}

// segmentMatch matches a single path segment against a pattern segment that
// may contain "*" (matches any characters within a segment, not "/").
func segmentMatch(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	// Simple wildcard: split on "*" and match prefix/suffix.
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	cur := s[len(parts[0]):]
	for _, part := range parts[1:] {
		idx := strings.Index(cur, part)
		if idx < 0 {
			return false
		}
		cur = cur[idx+len(part):]
	}
	return true
}

// containsString reports whether haystack contains needle.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// StaleSignal records one stale condition that fired for a page.
type StaleSignal struct {
	// PageID is the page that is stale.
	PageID string

	// ConditionIndex is the 0-based index of the StaleCondition in the
	// manifest's StaleWhen list that fired.
	ConditionIndex int

	// TriggeringSymbols is the subset of changed symbols that triggered
	// this condition.
	TriggeringSymbols []string

	// Kind describes the type of condition: "signature_change_in" or
	// "new_caller_added_to".
	Kind string
}

// EvaluateStaleConditions checks which stale_when conditions in manifest
// apply to the given changed pairs, returning one StaleSignal per condition
// that fired. The caller is responsible for injecting stale banners into the
// page AST; this function only detects and reports.
//
// The check is intentionally lightweight — it does not invoke the graph
// resolver, only compares against the symbol names listed in the conditions.
func EvaluateStaleConditions(m DependencyManifest, changed []ChangedPair) []StaleSignal {
	if len(m.StaleWhen) == 0 || len(changed) == 0 {
		return nil
	}

	// Build symbol set from changed pairs.
	changedSymbols := make(map[string]bool, len(changed))
	for _, cp := range changed {
		if cp.Symbol != "" {
			changedSymbols[cp.Symbol] = true
		}
	}

	var signals []StaleSignal

	for i, cond := range m.StaleWhen {
		switch {
		case len(cond.SignatureChangeIn) > 0:
			var triggered []string
			for _, sym := range cond.SignatureChangeIn {
				if changedSymbols[sym] {
					triggered = append(triggered, sym)
				}
			}
			if len(triggered) > 0 {
				signals = append(signals, StaleSignal{
					PageID:            m.PageID,
					ConditionIndex:    i,
					TriggeringSymbols: triggered,
					Kind:              "signature_change_in",
				})
			}

		case len(cond.NewCallerAddedTo) > 0:
			var triggered []string
			for _, sym := range cond.NewCallerAddedTo {
				if changedSymbols[sym] {
					triggered = append(triggered, sym)
				}
			}
			if len(triggered) > 0 {
				signals = append(signals, StaleSignal{
					PageID:            m.PageID,
					ConditionIndex:    i,
					TriggeringSymbols: triggered,
					Kind:              "new_caller_added_to",
				})
			}
		}
	}

	return signals
}
