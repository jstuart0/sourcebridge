// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleArtifact() *Artifact {
	return &Artifact{
		ID:           "art-1",
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusReady,
		Stale:        false,
		SourceRevision: SourceRevision{
			CommitSHA: "abc123",
			Branch:    "main",
		},
		Sections: []Section{
			{
				ID:         "sec-1",
				Title:      "System Purpose",
				Content:    "This system processes payments.",
				Summary:    "Processes payments.",
				Confidence: ConfidenceHigh,
				OrderIndex: 0,
				Evidence: []Evidence{
					{ID: "ev-1", SourceType: EvidenceFile, FilePath: "main.go", LineStart: 1, LineEnd: 10, Rationale: "Entry point"},
				},
			},
			{
				ID:         "sec-2",
				Title:      "Architecture",
				Content:    "Layered architecture with handlers and services.",
				Confidence: ConfidenceMedium,
				Inferred:   true,
				OrderIndex: 1,
			},
		},
	}
}

func TestExportJSON(t *testing.T) {
	artifact := sampleArtifact()
	content, mime, err := ExportArtifact(artifact, FormatJSON)
	if err != nil {
		t.Fatalf("ExportArtifact JSON: %v", err)
	}
	if mime != "application/json" {
		t.Fatalf("expected application/json, got %s", mime)
	}

	var parsed Artifact
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}
	if parsed.Type != ArtifactCliffNotes {
		t.Fatalf("expected cliff_notes, got %s", parsed.Type)
	}
	if len(parsed.Sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(parsed.Sections))
	}
	// Evidence must be preserved.
	if len(parsed.Sections[0].Evidence) != 1 {
		t.Fatalf("expected 1 evidence on first section, got %d", len(parsed.Sections[0].Evidence))
	}
	if parsed.Sections[0].Evidence[0].FilePath != "main.go" {
		t.Fatalf("expected main.go, got %s", parsed.Sections[0].Evidence[0].FilePath)
	}
}

func TestExportMarkdown(t *testing.T) {
	artifact := sampleArtifact()
	content, mime, err := ExportArtifact(artifact, FormatMarkdown)
	if err != nil {
		t.Fatalf("ExportArtifact Markdown: %v", err)
	}
	if mime != "text/markdown" {
		t.Fatalf("expected text/markdown, got %s", mime)
	}

	if !strings.Contains(content, "# Cliff Notes") {
		t.Fatal("expected markdown title")
	}
	if !strings.Contains(content, "## System Purpose") {
		t.Fatal("expected section heading")
	}
	if !strings.Contains(content, "**Confidence:** high") {
		t.Fatal("expected confidence annotation")
	}
	if !strings.Contains(content, "main.go:1-10") {
		t.Fatal("expected evidence file reference")
	}
	if !strings.Contains(content, "[Inferred]") {
		t.Fatal("expected inferred marker on second section")
	}
	if !strings.Contains(content, "abc123") {
		t.Fatal("expected source revision")
	}
}

func TestExportHTML(t *testing.T) {
	artifact := sampleArtifact()
	content, mime, err := ExportArtifact(artifact, FormatHTML)
	if err != nil {
		t.Fatalf("ExportArtifact HTML: %v", err)
	}
	if mime != "text/html" {
		t.Fatalf("expected text/html, got %s", mime)
	}

	if !strings.Contains(content, "<h1>Cliff Notes</h1>") {
		t.Fatal("expected HTML title")
	}
	if !strings.Contains(content, "<h2>System Purpose</h2>") {
		t.Fatal("expected section heading")
	}
	if !strings.Contains(content, "badge-high") {
		t.Fatal("expected confidence badge")
	}
	if !strings.Contains(content, "main.go:1-10") {
		t.Fatal("expected evidence reference")
	}
	if !strings.Contains(content, "badge-inferred") {
		t.Fatal("expected inferred badge")
	}
}

func TestExportStaleArtifact(t *testing.T) {
	artifact := sampleArtifact()
	artifact.Stale = true

	md, _, _ := ExportArtifact(artifact, FormatMarkdown)
	if !strings.Contains(md, "Stale") {
		t.Fatal("expected stale indicator in markdown")
	}

	htmlContent, _, _ := ExportArtifact(artifact, FormatHTML)
	if !strings.Contains(htmlContent, "badge-stale") {
		t.Fatal("expected stale badge in HTML")
	}
}

func TestExportNilArtifact(t *testing.T) {
	_, _, err := ExportArtifact(nil, FormatJSON)
	if err == nil {
		t.Fatal("expected error for nil artifact")
	}
}

func TestExportUnsupportedFormat(t *testing.T) {
	artifact := sampleArtifact()
	_, _, err := ExportArtifact(artifact, "pdf")
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
}
