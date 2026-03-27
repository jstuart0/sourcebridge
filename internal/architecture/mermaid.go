// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package architecture

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// SanitizeNodeID converts a file/module path into a valid Mermaid node ID.
func SanitizeNodeID(path string) string {
	id := nonAlphanumeric.ReplaceAllString(path, "_")
	if len(id) > 0 && id[0] >= '0' && id[0] <= '9' {
		id = "m_" + id
	}
	if id == "" {
		id = "unknown"
	}
	return id
}

// escapeLabel escapes double quotes within a Mermaid label.
func escapeLabel(s string) string {
	return strings.ReplaceAll(s, `"`, `#quot;`)
}

// GenerateModuleMermaid produces Mermaid flowchart source for a module-level diagram.
func GenerateModuleMermaid(modules []ModuleNode, truncated bool, totalModules int) string {
	var b strings.Builder

	layout := "LR"
	if len(modules) > 15 {
		layout = "TB"
	}

	b.WriteString(fmt.Sprintf("flowchart %s\n", layout))

	if truncated {
		b.WriteString(fmt.Sprintf("    %%%% Showing %d of %d modules\n", len(modules), totalModules))
	}

	// Node definitions
	for _, mod := range modules {
		id := SanitizeNodeID(mod.Path)
		label := escapeLabel(mod.Path)
		b.WriteString(fmt.Sprintf("    %s[\"%s\\n%d symbols\"]\n", id, label, mod.SymbolCount))
	}
	b.WriteString("\n")

	// Edge definitions
	nodeSet := make(map[string]bool)
	for _, mod := range modules {
		nodeSet[mod.Path] = true
	}

	for _, mod := range modules {
		srcID := SanitizeNodeID(mod.Path)
		for _, edge := range mod.OutboundEdges {
			if !nodeSet[edge.TargetPath] {
				continue
			}
			tgtID := SanitizeNodeID(edge.TargetPath)
			if edge.CallCount <= 1 {
				b.WriteString(fmt.Sprintf("    %s --> %s\n", srcID, tgtID))
			} else {
				b.WriteString(fmt.Sprintf("    %s -->|\"%d calls\"| %s\n", srcID, edge.CallCount, tgtID))
			}
		}
	}

	return b.String()
}

// GenerateFileMermaid produces Mermaid flowchart source for a file-level diagram within a module.
func GenerateFileMermaid(
	modulePrefix string,
	internalFiles []string,
	externalFiles []string,
	fileSymbolCount map[string]int,
	outboundEdges map[string][]EdgeInfo,
) string {
	var b strings.Builder

	layout := "LR"
	if len(internalFiles)+len(externalFiles) > 15 {
		layout = "TB"
	}

	b.WriteString(fmt.Sprintf("flowchart %s\n", layout))

	// Internal subgraph
	subgraphID := SanitizeNodeID(modulePrefix)
	b.WriteString(fmt.Sprintf("    subgraph %s[\"%s\"]\n", subgraphID, escapeLabel(modulePrefix)))
	for _, f := range internalFiles {
		id := SanitizeNodeID(f)
		baseName := filepath.Base(f)
		symCount := fileSymbolCount[f]
		if symCount > 0 {
			b.WriteString(fmt.Sprintf("        %s[\"%s\\n%d symbols\"]\n", id, escapeLabel(baseName), symCount))
		} else {
			b.WriteString(fmt.Sprintf("        %s[\"%s\"]\n", id, escapeLabel(baseName)))
		}
	}
	b.WriteString("    end\n\n")

	// External subgraph
	if len(externalFiles) > 0 {
		b.WriteString("    subgraph external[\" \"]\n")
		b.WriteString("        direction TB\n")
		for _, f := range externalFiles {
			id := SanitizeNodeID(f)
			label := escapeLabel(f)
			b.WriteString(fmt.Sprintf("        %s[\"%s\"]\n", id, label))
		}
		b.WriteString("    end\n\n")
	}

	// Edges
	allFiles := make(map[string]bool)
	for _, f := range internalFiles {
		allFiles[f] = true
	}
	for _, f := range externalFiles {
		allFiles[f] = true
	}

	for _, f := range internalFiles {
		srcID := SanitizeNodeID(f)
		for _, edge := range outboundEdges[f] {
			if !allFiles[edge.TargetPath] {
				continue
			}
			tgtID := SanitizeNodeID(edge.TargetPath)
			if edge.CallCount <= 1 {
				b.WriteString(fmt.Sprintf("    %s --> %s\n", srcID, tgtID))
			} else {
				b.WriteString(fmt.Sprintf("    %s -->|\"%d\"| %s\n", srcID, edge.CallCount, tgtID))
			}
		}
	}
	for _, f := range externalFiles {
		srcID := SanitizeNodeID(f)
		for _, edge := range outboundEdges[f] {
			if !allFiles[edge.TargetPath] {
				continue
			}
			tgtID := SanitizeNodeID(edge.TargetPath)
			if edge.CallCount <= 1 {
				b.WriteString(fmt.Sprintf("    %s --> %s\n", srcID, tgtID))
			} else {
				b.WriteString(fmt.Sprintf("    %s -->|\"%d\"| %s\n", srcID, edge.CallCount, tgtID))
			}
		}
	}

	// Style external subgraph
	if len(externalFiles) > 0 {
		b.WriteString("\n    style external fill:none,stroke:#475569,stroke-dasharray:5 5\n")
	}

	return b.String()
}
