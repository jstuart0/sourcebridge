# MCP tool registration

How to add a new tool to the SourceBridge MCP server.

## Overview

All MCP tools live in `internal/api/rest/`. Each tool is represented by an
`mcpTool` struct that pairs a definition (name, description, JSON schema) with
its dispatch handler. Both are registered together — they cannot drift
independently.

## The mcpTool struct

Defined in `internal/api/rest/mcp.go` (as of commit `89c85f3`):

```go
// mcpTool bundles a tool's MCP definition and its dispatch handler so they
// must be supplied together at registration time.
type mcpTool struct {
    Definition mcpToolDefinition
    Handler    mcpToolHandlerFunc
}
```

`mcpToolHandlerFunc` is the uniform handler signature:

```go
type mcpToolHandlerFunc func(h *mcpHandler, ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error)
```

Two adapter helpers reduce boilerplate:

- `noCtxHandler(fn)` — wraps handlers that don't need the request context
  (the majority of tools).
- `withCtxHandler(fn)` — wraps handlers that need context propagation
  (e.g. `explain_code`, `ask_question`, `search_symbols`).

## Registration flow

1. `newMCPHandlerWithEdition` constructs the handler.
2. It calls `registerCoreTools(h)`, then per-phase `register*Tools(h)` functions.
3. Each `register*Tools` function iterates a `[]mcpTool` slice and calls
   `h.registerTool(t)` for each entry.
4. `registerTool` panics on duplicate name — collisions surface at construction
   (which always runs in tests), not at runtime.

## Worked example: adding get_callers

`get_callers` is a core tool. Here is its registration pattern, condensed:

**1. Define the tool in `coreTools()` (or a dedicated `*ToolDefs()` helper):**

```go
// In mcp.go, inside h.coreTools():
{Definition: defByName["get_callers"], Handler: noCtxHandler((*mcpHandler).callGetCallers)},
```

`defByName` is built from `h.baseTools()` at the start of `coreTools()`, so
the definition is always sourced from the same place as the `baseTools()` listing.

**2. Implement the handler method on `*mcpHandler`:**

```go
// In mcp_accessors.go (or a file named for the tool group):
func (h *mcpHandler) callGetCallers(session *mcpSession, args json.RawMessage) (interface{}, error) {
    // parse args, call store, return result
}
```

**3. Add the tool definition to `baseTools()`:**

The tool must appear in the slice returned by `h.baseTools()` (in `mcp.go`).
`baseTools()` drives the `tools/list` response visible to MCP clients.

**4. Declare the capability in `internal/capabilities/registry_data.go`:**

Every tool maps to a capability. If you are adding a new capability (not just
a tool within an existing one), register it there.

**5. Update `TestRegistry_AllMCPToolsExistInBaseTools` and `TestDispatchMapCoversBaseTools`:**

Two tests enforce that the dispatch map and `baseTools()` stay in sync, in both
directions. Run `make test` — if either test fails, you missed step 3 or 2.

## Registration for per-phase tool groups

Tools added after the core set follow the same pattern but are registered in
their own `register*Tools` function rather than in `registerCoreTools`. Example
(requirement linking tools):

```go
func registerRequirementLinkingTools(h *mcpHandler) {
    for _, t := range h.requirementLinkingTools() {
        h.registerTool(t)
    }
}
```

`requirementLinkingTools()` returns `[]mcpTool` exactly as `coreTools()` does.
The function is called from `newMCPHandlerWithEdition` after `registerCoreTools`.

## Edition filtering

Tools that are enterprise-only are filtered out of `tools/list` in
`handleToolsListCtx` based on `h.capabilityChecker(capName, h.edition)`. If your
tool should be OSS-only or enterprise-only, set the appropriate edition in
`internal/capabilities/registry_data.go`.

## Related

- Plane ticket: [CA-155](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/797d0038-6493-49dc-8307-d7c54d3f6611/) (Phase 3 — MCP tool refactor)
- Plan: [`thoughts/shared/plans/2026-05-04-system-audit-refactor.md`](../../thoughts/shared/plans/2026-05-04-system-audit-refactor.md) Phase 3 Slice 1
- Code: [`internal/api/rest/mcp.go`](../../internal/api/rest/mcp.go) (structs, adapters, `newMCPHandlerWithEdition`)
- Capabilities registry: [`internal/capabilities/registry_data.go`](../../internal/capabilities/registry_data.go)

---
*Documented by scott on 2026-05-04.*
