// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
)

// ExportFormat identifies the output format for knowledge export.
type ExportFormat string

const (
	FormatJSON     ExportFormat = "json"
	FormatMarkdown ExportFormat = "markdown"
	FormatHTML     ExportFormat = "html"
)

// ExportArtifact exports a knowledge artifact in the requested format.
// Returns the rendered content and an appropriate MIME type.
func ExportArtifact(artifact *Artifact, format ExportFormat) (string, string, error) {
	if artifact == nil {
		return "", "", fmt.Errorf("artifact is nil")
	}

	// Sort sections by order index.
	sections := make([]Section, len(artifact.Sections))
	copy(sections, artifact.Sections)
	sort.Slice(sections, func(i, j int) bool { return sections[i].OrderIndex < sections[j].OrderIndex })
	artifact.Sections = sections

	switch format {
	case FormatJSON:
		return exportJSON(artifact)
	case FormatMarkdown:
		return exportMarkdown(artifact), "text/markdown", nil
	case FormatHTML:
		return exportHTML(artifact), "text/html", nil
	default:
		return "", "", fmt.Errorf("unsupported export format: %s", format)
	}
}

func exportJSON(artifact *Artifact) (string, string, error) {
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("JSON marshal: %w", err)
	}
	return string(data), "application/json", nil
}

func exportMarkdown(artifact *Artifact) string {
	var sb strings.Builder

	title := artifactTypeTitle(artifact.Type)
	sb.WriteString("# " + title + "\n\n")

	sb.WriteString(fmt.Sprintf("**Audience:** %s | **Depth:** %s", artifact.Audience, artifact.Depth))
	if artifact.Stale {
		sb.WriteString(" | **Status:** Stale")
	}
	sb.WriteString("\n\n")

	if artifact.SourceRevision.CommitSHA != "" {
		sb.WriteString(fmt.Sprintf("*Source revision: %s", artifact.SourceRevision.CommitSHA))
		if artifact.SourceRevision.Branch != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", artifact.SourceRevision.Branch))
		}
		sb.WriteString("*\n\n")
	}

	sb.WriteString("---\n\n")

	for _, sec := range artifact.Sections {
		sb.WriteString("## " + sec.Title + "\n\n")

		if sec.Inferred {
			sb.WriteString("*[Inferred]* ")
		}
		sb.WriteString(fmt.Sprintf("**Confidence:** %s\n\n", sec.Confidence))

		sb.WriteString(sec.Content + "\n\n")

		if len(sec.Evidence) > 0 {
			sb.WriteString("### Sources\n\n")
			for _, ev := range sec.Evidence {
				ref := formatEvidenceRef(ev)
				sb.WriteString("- " + ref + "\n")
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func exportHTML(artifact *Artifact) string {
	var sb strings.Builder

	title := artifactTypeTitle(artifact.Type)

	sb.WriteString("<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\">\n")
	sb.WriteString("<title>" + html.EscapeString(title) + "</title>\n")
	sb.WriteString("<style>body{font-family:system-ui,sans-serif;max-width:48rem;margin:2rem auto;padding:0 1rem;line-height:1.6;color:#1a1a1a}")
	sb.WriteString("h1{border-bottom:2px solid #e5e5e5;padding-bottom:.5rem}")
	sb.WriteString("h2{margin-top:2rem}")
	sb.WriteString(".meta{color:#666;font-size:.875rem}")
	sb.WriteString(".badge{display:inline-block;padding:.125rem .5rem;border-radius:1rem;font-size:.75rem;font-weight:500}")
	sb.WriteString(".badge-high{background:#dcfce7;color:#166534}")
	sb.WriteString(".badge-medium{background:#fef9c3;color:#854d0e}")
	sb.WriteString(".badge-low{background:#fee2e2;color:#991b1b}")
	sb.WriteString(".badge-stale{background:#f3f4f6;color:#6b7280;border:1px solid #d1d5db}")
	sb.WriteString(".badge-inferred{background:#eff6ff;color:#1e40af}")
	sb.WriteString(".evidence{margin-top:.75rem;padding:.5rem .75rem;background:#f9fafb;border:1px solid #e5e7eb;border-radius:.375rem;font-size:.875rem}")
	sb.WriteString(".evidence code{font-family:monospace;color:#7c3aed}")
	sb.WriteString("</style>\n</head><body>\n")

	sb.WriteString("<h1>" + html.EscapeString(title) + "</h1>\n")

	sb.WriteString("<p class=\"meta\">")
	sb.WriteString("Audience: " + html.EscapeString(string(artifact.Audience)))
	sb.WriteString(" &middot; Depth: " + html.EscapeString(string(artifact.Depth)))
	if artifact.Stale {
		sb.WriteString(" &middot; <span class=\"badge badge-stale\">Stale</span>")
	}
	sb.WriteString("</p>\n")

	if artifact.SourceRevision.CommitSHA != "" {
		sb.WriteString("<p class=\"meta\">Source revision: <code>" + html.EscapeString(artifact.SourceRevision.CommitSHA) + "</code>")
		if artifact.SourceRevision.Branch != "" {
			sb.WriteString(" (" + html.EscapeString(artifact.SourceRevision.Branch) + ")")
		}
		sb.WriteString("</p>\n")
	}

	sb.WriteString("<hr>\n")

	for _, sec := range artifact.Sections {
		sb.WriteString("<h2>" + html.EscapeString(sec.Title) + "</h2>\n")

		sb.WriteString("<p>")
		badgeClass := "badge-" + string(sec.Confidence)
		sb.WriteString("<span class=\"badge " + badgeClass + "\">" + html.EscapeString(string(sec.Confidence)) + "</span>")
		if sec.Inferred {
			sb.WriteString(" <span class=\"badge badge-inferred\">inferred</span>")
		}
		sb.WriteString("</p>\n")

		// Convert newlines in content to paragraphs.
		paragraphs := strings.Split(sec.Content, "\n\n")
		for _, p := range paragraphs {
			p = strings.TrimSpace(p)
			if p != "" {
				sb.WriteString("<p>" + html.EscapeString(p) + "</p>\n")
			}
		}

		if len(sec.Evidence) > 0 {
			sb.WriteString("<div class=\"evidence\">\n<strong>Sources:</strong><ul>\n")
			for _, ev := range sec.Evidence {
				ref := formatEvidenceRef(ev)
				sb.WriteString("<li><code>" + html.EscapeString(ref) + "</code></li>\n")
			}
			sb.WriteString("</ul></div>\n")
		}
	}

	sb.WriteString("</body></html>\n")
	return sb.String()
}

func artifactTypeTitle(t ArtifactType) string {
	switch t {
	case ArtifactCliffNotes:
		return "Cliff Notes"
	case ArtifactLearningPath:
		return "Learning Path"
	case ArtifactCodeTour:
		return "Code Tour"
	default:
		return string(t)
	}
}

func formatEvidenceRef(ev Evidence) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("[%s]", ev.SourceType))
	if ev.FilePath != "" {
		ref := ev.FilePath
		if ev.LineStart > 0 {
			ref += fmt.Sprintf(":%d", ev.LineStart)
			if ev.LineEnd > 0 && ev.LineEnd != ev.LineStart {
				ref += fmt.Sprintf("-%d", ev.LineEnd)
			}
		}
		parts = append(parts, ref)
	}
	if ev.Rationale != "" {
		parts = append(parts, "— "+ev.Rationale)
	}
	return strings.Join(parts, " ")
}
