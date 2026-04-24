// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package capabilities

import (
	"strings"
	"testing"
)

// TestRegistry_NoDuplicateNames fails if two entries share a Name —
// capabilities are queried by name and duplicates would make the
// lookup nondeterministic.
func TestRegistry_NoDuplicateNames(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Registry {
		if seen[c.Name] {
			t.Errorf("duplicate capability name %q", c.Name)
		}
		seen[c.Name] = true
	}
}

// TestRegistry_NamesAreStable asserts names use snake_case and don't
// contain spaces or caps — they're stable public identifiers used in
// MCP initialize responses and client code.
func TestRegistry_NamesAreStable(t *testing.T) {
	for _, c := range Registry {
		if c.Name == "" {
			t.Error("empty capability name")
			continue
		}
		if strings.ContainsAny(c.Name, " \t\n") {
			t.Errorf("capability name %q contains whitespace", c.Name)
		}
		if strings.ToLower(c.Name) != c.Name {
			t.Errorf("capability name %q is not lowercase", c.Name)
		}
	}
}

// TestRegistry_EveryCapHasEditions fails if a capability declares no
// editions — that means it's declared but unreachable.
func TestRegistry_EveryCapHasEditions(t *testing.T) {
	for _, c := range Registry {
		if len(c.Editions) == 0 {
			t.Errorf("capability %q has empty Editions — declaration has no effect", c.Name)
		}
	}
}

// TestRegistry_NoDuplicateMCPTools asserts no single MCP tool name
// appears under two capabilities — each tool must be gated by at
// most one capability.
func TestRegistry_NoDuplicateMCPTools(t *testing.T) {
	seen := map[string]string{} // tool name → capability name
	for _, c := range Registry {
		for _, toolName := range c.MCPToolNames {
			if owner, ok := seen[toolName]; ok {
				t.Errorf("MCP tool %q gated by two capabilities: %q and %q", toolName, owner, c.Name)
			}
			seen[toolName] = c.Name
		}
	}
}

// TestAvailable_OSSExcludesEnterprise verifies the basic edition
// filter: an OSS session shouldn't see enterprise-only capabilities.
func TestAvailable_OSSExcludesEnterprise(t *testing.T) {
	names := map[string]bool{}
	for _, n := range AvailableNames(EditionOSS) {
		names[n] = true
	}

	// enterprise_reports is enterprise-only.
	if names["enterprise_reports"] {
		t.Error("OSS availability list should not include enterprise_reports")
	}
	// hybrid_search is in both editions.
	if !names["hybrid_search"] {
		t.Error("OSS availability list missing hybrid_search")
	}
}

// TestAvailable_EnterpriseIncludesBoth verifies enterprise sees both
// OSS and enterprise capabilities.
func TestAvailable_EnterpriseIncludesBoth(t *testing.T) {
	names := map[string]bool{}
	for _, n := range AvailableNames(EditionEnterprise) {
		names[n] = true
	}
	if !names["enterprise_reports"] {
		t.Error("enterprise availability list missing enterprise_reports")
	}
	if !names["hybrid_search"] {
		t.Error("enterprise availability list missing hybrid_search (OSS capability)")
	}
}

// TestMCPToolGatedBy_Lookup asserts the lookup returns the expected
// capability for a sample tool name, and nil for unknown tools.
func TestMCPToolGatedBy_Lookup(t *testing.T) {
	cap := MCPToolGatedBy("get_callers")
	if cap == nil {
		t.Fatal("expected capability to gate get_callers")
	}
	if cap.Name != "call_graph" {
		t.Errorf("expected call_graph, got %q", cap.Name)
	}

	if got := MCPToolGatedBy("nonexistent_tool"); got != nil {
		t.Errorf("expected nil for nonexistent tool, got %q", got.Name)
	}
}

// TestNormalizeEdition handles the config-string variants.
func TestNormalizeEdition(t *testing.T) {
	cases := []struct {
		in   string
		want Edition
	}{
		{"enterprise", EditionEnterprise},
		{"Enterprise", EditionEnterprise},
		{"ENTERPRISE", EditionEnterprise},
		{" enterprise ", EditionEnterprise},
		{"oss", EditionOSS},
		{"", EditionOSS},
		{"community", EditionOSS},
	}
	for _, c := range cases {
		if got := NormalizeEdition(c.in); got != c.want {
			t.Errorf("NormalizeEdition(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
