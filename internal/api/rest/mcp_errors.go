// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"errors"
	"fmt"
)

// Phase 2.6 — structured MCP tool errors.
//
// Back-compat envelope: every tool error returns
//   { isError: true, content: [{type: "text", text: human-readable}], _meta: {sourcebridge: {...}} }
//
// Vanilla MCP clients read the `content[].text` and render it as-is —
// they don't know about `_meta`. Capability-aware clients read
// `_meta.sourcebridge.{code, remediation, retry_after_sec?}` and can
// act programmatically (retry with backoff, fall back to another
// tool, surface a specific user-visible message).
//
// Tool handlers return a *mcpToolError — the dispatch layer detects
// this type and populates the envelope. Handlers that return a plain
// `error` still work (the envelope omits `_meta`), so migration is
// incremental.

// MCP tool error codes. These are stable public identifiers; clients
// compare against the string values, so don't rename after release.
const (
	MCPErrRepositoryNotIndexed = "REPOSITORY_NOT_INDEXED"
	MCPErrRepositoryStale      = "REPOSITORY_STALE"
	MCPErrSymbolNotFound       = "SYMBOL_NOT_FOUND"
	MCPErrModelUnavailable     = "MODEL_UNAVAILABLE"
	MCPErrQueryTooBroad        = "QUERY_TOO_BROAD"
	MCPErrCapabilityDisabled   = "CAPABILITY_DISABLED"
	MCPErrRateLimited          = "RATE_LIMITED"
	MCPErrInvalidArguments     = "INVALID_ARGUMENTS"
)

// mcpToolError is a typed error carrying the structured envelope fields.
// Handlers that want structured errors return one directly; the
// dispatch layer unwraps and populates the envelope.
type mcpToolError struct {
	Code          string // stable identifier — see MCPErr* constants
	Message       string // human-readable, complete on its own
	Remediation   string // optional — specific action the caller can take
	RetryAfterSec int    // optional — for RATE_LIMITED responses (0 = omit)
}

// Error implements the error interface — returns the human-readable
// message so plain `error` callers still get useful output.
func (e *mcpToolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// asMCPToolError attempts to unwrap an error to an *mcpToolError. Returns
// nil if the error isn't structured.
func asMCPToolError(err error) *mcpToolError {
	if err == nil {
		return nil
	}
	var e *mcpToolError
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// toolErrorMeta builds the `_meta.sourcebridge` map for a structured
// error. Returns nil when the error has no structured payload (so the
// envelope omits `_meta` entirely).
func toolErrorMeta(err error) map[string]interface{} {
	e := asMCPToolError(err)
	if e == nil || e.Code == "" {
		return nil
	}
	inner := map[string]interface{}{
		"code": e.Code,
	}
	if e.Remediation != "" {
		inner["remediation"] = e.Remediation
	}
	if e.RetryAfterSec > 0 {
		inner["retry_after_sec"] = e.RetryAfterSec
	}
	return map[string]interface{}{"sourcebridge": inner}
}

// ---------------------------------------------------------------------------
// Convenience constructors
// ---------------------------------------------------------------------------

func errRepositoryNotIndexed(repoID string) *mcpToolError {
	return &mcpToolError{
		Code:        MCPErrRepositoryNotIndexed,
		Message:     fmt.Sprintf("Repository %s is not indexed. Call index_repository before using this tool.", repoID),
		Remediation: "Call index_repository for this repository, then retry.",
	}
}

func errSymbolNotFound(name, filePath string) *mcpToolError {
	return &mcpToolError{
		Code:        MCPErrSymbolNotFound,
		Message:     fmt.Sprintf("Symbol %q was not found in %s.", name, filePath),
		Remediation: "Verify the file_path and symbol_name; call search_symbols to find similar matches.",
	}
}

func errModelUnavailable(detail string) *mcpToolError {
	msg := "The configured LLM is unreachable."
	if detail != "" {
		msg = msg + " " + detail
	}
	return &mcpToolError{
		Code:        MCPErrModelUnavailable,
		Message:     msg,
		Remediation: "Degrade to non-LLM tools (search_symbols, get_callers, …) or retry after the provider recovers.",
	}
}

func errCapabilityDisabled(capName string) *mcpToolError {
	return &mcpToolError{
		Code:        MCPErrCapabilityDisabled,
		Message:     fmt.Sprintf("This tool requires the %q capability, which is not enabled for this edition.", capName),
		Remediation: "Upgrade to an edition that includes this capability, or use an available alternative.",
	}
}

func errInvalidArguments(detail string) *mcpToolError {
	return &mcpToolError{
		Code:        MCPErrInvalidArguments,
		Message:     "Invalid arguments: " + detail,
		Remediation: "Check the tool's input_schema in tools/list and retry with valid parameters.",
	}
}
