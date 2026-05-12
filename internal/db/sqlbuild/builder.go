// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package sqlbuild accumulates SQL SET-clause assignments and their bound
// variables for SurrealDB write paths.
//
// The CA-179 load-bearing invariant — "nil pointers are never serialised
// as JSON null on the wire" — is preserved by every Add* helper: when the
// guard (nil pointer, empty string, or explicit `present` flag) signals
// absence, the column is skipped entirely. Callers therefore cannot
// accidentally bind a JSON null to an `option<…>` SurrealDB column.
//
// Builder is a small consolidation of four ad-hoc reimplementations of
// the same pattern: UpdateRepositoryMeta (empty-string sentinel),
// UpdateRequirementFields (nil-pointer + helpers), SetModelCapabilities
// (nil-pointer + datetime wrapping), buildProfileFieldClauses (explicit
// presence flag + per-call var-key prefix + special-case raw clause).
// Each variant is supported by a dedicated Add* method; the consolidated
// home avoids future drift.
package sqlbuild

import (
	"strings"
	"time"

	"github.com/surrealdb/surrealdb.go/pkg/models"
)

// Builder accumulates SET-clause assignments. The zero value is usable
// via the New / Prefixed constructors. Methods are NOT concurrency-safe;
// callers build a Builder on a single goroutine, then read Clause and
// Vars for SQL interpolation.
type Builder struct {
	prefix  string
	clauses []string
	vars    map[string]any
}

// New returns a Builder with no prefix; bind keys equal column names.
// Use for callers whose SET fragment is interpolated once (single-arm
// UPDATE statements). The dual-arm pattern in SetModelCapabilities also
// works with a non-prefixed Builder because both arms interpolate the
// same fragment string.
func New() *Builder {
	return &Builder{vars: map[string]any{}}
}

// Prefixed returns a Builder whose bind keys are namespaced with the
// given prefix. Used by buildProfileFieldClauses so the profile-arm and
// legacy-arm batches can share a single SurrealDB BEGIN/COMMIT without
// var-name collisions ($profile_provider vs $legacy_provider).
func Prefixed(prefix string) *Builder {
	return &Builder{prefix: prefix, vars: map[string]any{}}
}

// Len returns the number of accumulated clauses, including AddRaw entries.
func (b *Builder) Len() int {
	return len(b.clauses)
}

// Clause returns the comma-joined SET fragment ready for interpolation.
// Empty when no clauses have been added. The fragment does NOT include a
// leading SET keyword or any trailing comma.
func (b *Builder) Clause() string {
	return strings.Join(b.clauses, ", ")
}

// Clauses returns a copy of the raw clause slice for callers that need
// to merge with externally-managed static clauses (SetModelCapabilities).
func (b *Builder) Clauses() []string {
	out := make([]string, len(b.clauses))
	copy(out, b.clauses)
	return out
}

// Vars returns the bound-variables map. The map is owned by the Builder;
// callers MUST NOT mutate keys that match an emitted clause. To inject
// additional static variables (e.g., the WHERE-clause $id), merge AFTER
// constructing the Builder or pass them in a copy.
func (b *Builder) Vars() map[string]any {
	return b.vars
}

// bind is the single canonical write path: append "col = $key" and bind
// vars[key] = val. The key may be prefixed for namespacing.
func (b *Builder) bind(col string, val any) {
	key := b.prefix + col
	b.clauses = append(b.clauses, col+" = $"+key)
	b.vars[key] = val
}

// AddNonEmptyString skips empty strings. Use for callers (UpdateRepositoryMeta)
// that treat "" as the absent sentinel rather than nil-pointer.
func (b *Builder) AddNonEmptyString(col, val string) {
	if val == "" {
		return
	}
	b.bind(col, val)
}

// AddStringPtr skips nil pointers. Use for partial-update payloads where
// each field is `*string` (UpdateRequirementFields).
func (b *Builder) AddStringPtr(col string, val *string) {
	if val == nil {
		return
	}
	b.bind(col, *val)
}

// AddStringsPtr skips nil pointers; binds a non-nil `*[]string` even when
// the slice is empty (callers use empty-slice for "clear" intent).
func (b *Builder) AddStringsPtr(col string, val *[]string) {
	if val == nil {
		return
	}
	b.bind(col, *val)
}

// AddFloat64Ptr skips nil pointers. Use for `option<float>` SurrealDB columns.
func (b *Builder) AddFloat64Ptr(col string, val *float64) {
	if val == nil {
		return
	}
	b.bind(col, *val)
}

// AddTimePtr skips nil pointers; wraps the dereferenced time.Time in
// models.CustomDateTime so the SurrealDB CBOR codec emits a tag-12
// datetime instead of an RFC3339 string. CA-179 constraint: `option<datetime>`
// columns reject RFC3339 string bindings on SurrealDB v2.6.5.
func (b *Builder) AddTimePtr(col string, val *time.Time) {
	if val == nil {
		return
	}
	b.bind(col, models.CustomDateTime{Time: *val})
}

// AddPresent binds the column only when `present` is true, regardless of
// the value's type or zero-ness. Use for callers (buildProfileFieldClauses)
// that gate inclusion via an explicit `FieldsPresent` flag rather than
// the type-system zero-value.
func (b *Builder) AddPresent(col string, present bool, val any) {
	if !present {
		return
	}
	b.bind(col, val)
}

// AddRaw appends a literal SET fragment with no variable binding. Use
// sparingly for clauses that must emit a literal (api_key = '') or that
// need a SurrealDB function call (updated_at = time::now()).
func (b *Builder) AddRaw(clause string) {
	b.clauses = append(b.clauses, clause)
}
