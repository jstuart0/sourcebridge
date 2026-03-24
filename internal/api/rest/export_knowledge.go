// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

func (s *Server) handleExportKnowledgeArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := chi.URLParam(r, "id")
	if artifactID == "" {
		http.Error(w, `{"error":"artifact id is required"}`, http.StatusBadRequest)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	if s.knowledgeStore == nil {
		http.Error(w, `{"error":"knowledge store not configured"}`, http.StatusServiceUnavailable)
		return
	}

	artifact := s.knowledgeStore.GetKnowledgeArtifact(artifactID)
	if artifact == nil {
		http.Error(w, `{"error":"artifact not found"}`, http.StatusNotFound)
		return
	}

	// Hydrate sections and evidence.
	sections := s.knowledgeStore.GetKnowledgeSections(artifactID)
	for i := range sections {
		sections[i].Evidence = s.knowledgeStore.GetKnowledgeEvidence(sections[i].ID)
	}
	artifact.Sections = sections

	exportFormat := knowledge.ExportFormat(format)
	content, contentType, err := knowledge.ExportArtifact(artifact, exportFormat)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	typeName := string(artifact.Type)
	var ext string
	switch exportFormat {
	case knowledge.FormatJSON:
		ext = "json"
	case knowledge.FormatMarkdown:
		ext = "md"
	case knowledge.FormatHTML:
		ext = "html"
	default:
		ext = "txt"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-%s.%s", typeName, artifactID, ext))
	fmt.Fprint(w, content)
}
