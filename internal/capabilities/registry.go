// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package capabilities provides a single source of truth for feature
// gating across the GraphQL resolver, the REST edition checks, and
// the MCP server's tools/list + initialize response.
//
// Before this package existed, each surface had its own edition check
// (cfg.Edition == "enterprise" in REST handlers, conditional fields
// in GraphQL resolvers, ad-hoc filtering in MCP baseTools). That
// invited drift — GraphQL would report feature X as available while
// MCP said it wasn't, or REST would accept a request that GraphQL
// had silently disabled.
//
// The registry is the only place capabilities are declared. Every
// surface reads from it:
//
//   import "github.com/sourcebridge/sourcebridge/internal/capabilities"
//
//   if capabilities.IsAvailable("cross_repo_impact", edition) { ... }
//
// New capabilities are added by appending to Registry (see registry_data.go).
// Surfaces query by name; they never branch on edition strings directly.
package capabilities

import (
	"sort"
	"strings"
)

// Edition represents a SourceBridge distribution edition.
type Edition string

const (
	EditionOSS        Edition = "oss"
	EditionEnterprise Edition = "enterprise"
)

// NormalizeEdition coerces any input (config strings, JWT claims,
// env vars) to a valid Edition, defaulting to OSS.
func NormalizeEdition(s string) Edition {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "enterprise":
		return EditionEnterprise
	default:
		return EditionOSS
	}
}

// Capability describes a single feature the server may offer. The
// fields a capability carries are additive — a new consumer (a new
// GraphQL field, a new MCP tool, a new REST endpoint) reads the
// slice it cares about and ignores the rest.
type Capability struct {
	// Name is the stable identifier. Surfaces query by name.
	Name string

	// Description is a short human-readable blurb used in
	// `initialize` responses and admin surfaces.
	Description string

	// Editions lists the editions that offer the capability. An
	// empty list is treated as "no editions" (not "all").
	Editions []Edition

	// MCPToolNames lists MCP tools that depend on this capability.
	// MCP's tools/list filter reads this list to hide tools on
	// editions that don't offer the capability.
	MCPToolNames []string

	// GraphQLFields lists GraphQL field names that depend on this
	// capability. Used by the resolver's availability check and by
	// the drift-detection linter (see registry_test.go).
	GraphQLFields []string

	// RESTRoutes lists REST path prefixes that depend on this
	// capability. Used by the router's gating layer.
	RESTRoutes []string

	// RequiresModel is true when the capability needs an LLM
	// provider configured to function (e.g. ask_question needs one).
	// Surfaces can surface a user-friendly "no model configured"
	// error instead of generic 500s.
	RequiresModel bool

	// LatencyClass is a hint for agents using capability-aware
	// clients. One of "fast_read", "search", "llm", "indexing_op".
	LatencyClass string
}

// Available returns every capability offered by the given edition.
// Useful for building `initialize` responses.
func Available(edition Edition) []Capability {
	out := make([]Capability, 0, len(Registry))
	for _, c := range Registry {
		if !editionMatches(c.Editions, edition) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AvailableNames returns just the capability names available for the
// edition, sorted. Convenience for surfaces that only want names
// (e.g. MCP's sourcebridge.features list).
func AvailableNames(edition Edition) []string {
	caps := Available(edition)
	names := make([]string, len(caps))
	for i, c := range caps {
		names[i] = c.Name
	}
	return names
}

// IsAvailable reports whether a named capability is offered by the edition.
func IsAvailable(name string, edition Edition) bool {
	for _, c := range Registry {
		if c.Name == name {
			return editionMatches(c.Editions, edition)
		}
	}
	return false
}

// MCPToolGatedBy returns the capability (if any) that gates the
// given MCP tool name. MCP's tools/list filter uses this to decide
// whether to surface the tool.
func MCPToolGatedBy(toolName string) *Capability {
	for i, c := range Registry {
		for _, t := range c.MCPToolNames {
			if t == toolName {
				return &Registry[i]
			}
		}
	}
	return nil
}

// GraphQLFieldGatedBy returns the capability (if any) that gates the
// given GraphQL field name.
func GraphQLFieldGatedBy(fieldName string) *Capability {
	for i, c := range Registry {
		for _, f := range c.GraphQLFields {
			if f == fieldName {
				return &Registry[i]
			}
		}
	}
	return nil
}

// RESTRouteGatedBy returns the capability (if any) that gates a given
// REST route path prefix. Matches on longest-prefix so "/api/v1/enterprise/reports"
// matches an entry for "/api/v1/enterprise/reports" in preference to a
// shorter entry like "/api/v1/enterprise".
func RESTRouteGatedBy(path string) *Capability {
	var match *Capability
	var longest int
	for i, c := range Registry {
		for _, r := range c.RESTRoutes {
			if strings.HasPrefix(path, r) && len(r) > longest {
				longest = len(r)
				match = &Registry[i]
			}
		}
	}
	return match
}

func editionMatches(editions []Edition, want Edition) bool {
	for _, e := range editions {
		if e == want {
			return true
		}
	}
	return false
}
