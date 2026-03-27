// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package architecture

import (
	"strings"
	"testing"
)

func TestSanitizeNodeID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"internal/api", "internal_api"},
		{"internal/api/graphql", "internal_api_graphql"},
		{"web/src", "web_src"},
		{"(root)", "_root_"},
		{"(other)", "_other_"},
		{"3rd-party/lib", "m_3rd_party_lib"},
		{"simple", "simple"},
	}
	for _, tt := range tests {
		got := SanitizeNodeID(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeNodeID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMermaidEdgeLabels(t *testing.T) {
	modules := []ModuleNode{
		{
			Path:        "a",
			SymbolCount: 5,
			OutboundEdges: []EdgeInfo{
				{TargetPath: "b", CallCount: 1},
				{TargetPath: "c", CallCount: 10},
			},
		},
		{Path: "b", SymbolCount: 3},
		{Path: "c", SymbolCount: 8},
	}

	mermaid := GenerateModuleMermaid(modules, false, 3)

	// Single call should NOT have a label
	if strings.Contains(mermaid, `"1 calls"`) {
		t.Error("expected no label for call count = 1")
	}
	// Multi-call should have a label
	if !strings.Contains(mermaid, `"10 calls"`) {
		t.Error("expected label '10 calls' for edge a -> c")
	}
	// Both edges should exist
	if !strings.Contains(mermaid, "a --> b") {
		t.Error("expected edge a --> b")
	}
	if !strings.Contains(mermaid, "a -->") && !strings.Contains(mermaid, "c") {
		t.Error("expected edge a --> c with label")
	}
}

func TestMermaidLayoutSelection(t *testing.T) {
	// <= 15 modules: LR
	small := make([]ModuleNode, 10)
	for i := range small {
		small[i] = ModuleNode{Path: string(rune('a' + i)), SymbolCount: 1}
	}
	mermaidSmall := GenerateModuleMermaid(small, false, 10)
	if !strings.HasPrefix(mermaidSmall, "flowchart LR") {
		t.Error("expected LR layout for <= 15 modules")
	}

	// > 15 modules: TB
	large := make([]ModuleNode, 20)
	for i := range large {
		large[i] = ModuleNode{Path: string(rune('a'+i%26)) + string(rune('0'+i/26)), SymbolCount: 1}
	}
	mermaidLarge := GenerateModuleMermaid(large, false, 20)
	if !strings.HasPrefix(mermaidLarge, "flowchart TB") {
		t.Error("expected TB layout for > 15 modules")
	}
}

func TestMermaidSyntaxValid(t *testing.T) {
	modules := []ModuleNode{
		{
			Path:        "internal/api",
			SymbolCount: 42,
			OutboundEdges: []EdgeInfo{
				{TargetPath: "internal/db", CallCount: 18},
			},
		},
		{Path: "internal/db", SymbolCount: 38},
		{Path: "workers", SymbolCount: 23},
	}

	mermaid := GenerateModuleMermaid(modules, false, 3)

	// Verify basic structure
	if !strings.HasPrefix(mermaid, "flowchart") {
		t.Error("expected flowchart directive")
	}
	if !strings.Contains(mermaid, `internal_api["internal/api\n42 symbols"]`) {
		t.Errorf("expected node definition for internal/api, got:\n%s", mermaid)
	}
	if !strings.Contains(mermaid, `internal_db["internal/db\n38 symbols"]`) {
		t.Error("expected node definition for internal/db")
	}
	if !strings.Contains(mermaid, `internal_api -->|"18 calls"| internal_db`) {
		t.Errorf("expected edge with label, got:\n%s", mermaid)
	}
}

func TestMermaidTruncationComment(t *testing.T) {
	modules := []ModuleNode{
		{Path: "a", SymbolCount: 1},
	}
	mermaid := GenerateModuleMermaid(modules, true, 10)
	if !strings.Contains(mermaid, "Showing 1 of 10") {
		t.Error("expected truncation comment")
	}
}

func TestGenerateFileMermaid(t *testing.T) {
	internal := []string{"internal/db/store.go", "internal/db/surreal.go"}
	external := []string{"internal/api/resolver.go"}
	symCount := map[string]int{
		"internal/db/store.go":   18,
		"internal/db/surreal.go": 5,
	}
	edges := map[string][]EdgeInfo{
		"internal/db/store.go": {
			{TargetPath: "internal/db/surreal.go", CallCount: 12},
		},
		"internal/api/resolver.go": {
			{TargetPath: "internal/db/store.go", CallCount: 23},
		},
	}

	mermaid := GenerateFileMermaid("internal/db", internal, external, symCount, edges)

	if !strings.Contains(mermaid, `subgraph internal_db["internal/db"]`) {
		t.Errorf("expected internal subgraph, got:\n%s", mermaid)
	}
	if !strings.Contains(mermaid, `store.go\n18 symbols`) {
		t.Error("expected file node with symbol count")
	}
	if !strings.Contains(mermaid, "external") {
		t.Error("expected external subgraph")
	}
	if !strings.Contains(mermaid, "stroke-dasharray") {
		t.Error("expected dashed style for external subgraph")
	}
}
