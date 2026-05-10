// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import "context"

import (
	"fmt"
	"strings"
)

// buildDeepContextMarkdown assembles the deep-mode context block the
// Python worker's synthesis template consumes. Section order is
// load-bearing: summaries first (highest-signal overview), then graph
// evidence (one-hop neighborhood around the caller-supplied symbol),
// then code snippets (file-level evidence), then related requirements.
//
// Kept as a pure function so golden-fixture tests (§Prompt Assembly
// Responsibility) can lock the handoff string across the Go/Python
// boundary.
func buildDeepContextMarkdown(
	in AskInput,
	summaries []SummaryEvidence,
	files []FileEvidence,
	neighbors []GraphNeighbor,
	requirementLines []string,
) string {
	var sb strings.Builder

	if len(summaries) > 0 {
		sb.WriteString("# Understanding summaries\n\n")
		for _, s := range summaries {
			headline := s.Headline
			if headline == "" {
				headline = s.UnitID
			}
			fmt.Fprintf(&sb, "## %s\n", headline)
			sb.WriteString(s.SummaryText)
			fmt.Fprintf(&sb, "\n(source: ca_summary_node/%s)\n\n", s.UnitID)
		}
	}

	if len(neighbors) > 0 {
		sb.WriteString("# Graph evidence\n\n")
		for _, n := range neighbors {
			if n.FilePath != "" {
				fmt.Fprintf(&sb, "- %s at %s:%d-%d\n", n.QualifiedName, n.FilePath, n.StartLine, n.EndLine)
			} else {
				fmt.Fprintf(&sb, "- %s\n", n.QualifiedName)
			}
		}
		sb.WriteString("\n")
	}

	if len(files) > 0 {
		sb.WriteString("# Code snippets\n\n")
		for _, f := range files {
			lang := extFromPath(f.Path)
			fmt.Fprintf(&sb, "## %s:%d-%d\n", f.Path, f.StartLine, f.EndLine)
			if f.Reason != "" {
				fmt.Fprintf(&sb, "_signals: %s_\n", f.Reason)
			}
			fmt.Fprintf(&sb, "```%s\n%s\n```\n\n", lang, f.Snippet)
		}
	}

	// Caller-supplied code (via AskInput.Code) is included in addition
	// to retrieved files — the user hand-selected it so it carries
	// extra signal.
	if in.Code != "" {
		sb.WriteString("# Caller-supplied code\n\n")
		if in.FilePath != "" {
			fmt.Fprintf(&sb, "## %s\n", in.FilePath)
		}
		fmt.Fprintf(&sb, "```%s\n%s\n", in.Language, in.Code)
		if !strings.HasSuffix(in.Code, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n\n")
	}

	if len(requirementLines) > 0 {
		sb.WriteString("# Related requirements\n\n")
		for _, line := range requirementLines {
			fmt.Fprintf(&sb, "- %s\n", line)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// collectGraphNeighbors pulls callers + callees for the supplied focal
// symbol, bounded by `cap` per direction. Returns nil when no focal
// symbol was provided (deep mode without a symbol pin).
func collectGraphNeighbors(ctx context.Context, g GraphExpander, focalSymbolID string, cap int) []GraphNeighbor {
	if g == nil || focalSymbolID == "" {
		return nil
	}
	out := make([]GraphNeighbor, 0, cap*2)
	callers := g.GetCallers(ctx, focalSymbolID)
	if len(callers) > cap {
		callers = callers[:cap]
	}
	out = append(out, callers...)
	callees := g.GetCallees(ctx, focalSymbolID)
	if len(callees) > cap {
		callees = callees[:cap]
	}
	out = append(out, callees...)
	return out
}

// extFromPath returns the language-hint for markdown fenced code
// blocks. Only a short allow-list — unknown extensions fence with no
// language tag so renderers don't try to highlight garbage.
func extFromPath(path string) string {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return ""
	}
	ext := strings.ToLower(path[i+1:])
	switch ext {
	case "go":
		return "go"
	case "py":
		return "python"
	case "ts":
		return "typescript"
	case "tsx":
		return "tsx"
	case "js":
		return "javascript"
	case "jsx":
		return "jsx"
	case "java":
		return "java"
	case "rs":
		return "rust"
	case "md":
		return "markdown"
	}
	return ""
}
