// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/sourcebridge/sourcebridge/internal/architecture"
)

// diagramDocumentStore is a simple in-memory store for diagram documents.
type diagramDocumentStore struct {
	mu   sync.RWMutex
	docs map[string]*architecture.DiagramDocument // key: repoID + ":" + sourceKind
}

var diagStore = &diagramDocumentStore{
	docs: make(map[string]*architecture.DiagramDocument),
}

func diagramKey(repoID string, sourceKind architecture.SourceKind) string {
	return repoID + ":" + string(sourceKind)
}

// handleGetStructuredDiagram returns the deterministic diagram as a DiagramDocument.
func (s *Server) handleGetStructuredDiagram(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	// Check for a user-edited version first
	diagStore.mu.RLock()
	if doc, ok := diagStore.docs[diagramKey(repoID, architecture.SourceUserEdited)]; ok {
		diagStore.mu.RUnlock()
		respondJSON(w, http.StatusOK, doc)
		return
	}
	diagStore.mu.RUnlock()

	store := s.getStore(r)

	opts := architecture.DiagramOpts{
		RepoID:      repoID,
		Level:       "MODULE",
		ModuleDepth: 1,
		MaxNodes:    30,
	}

	result, err := architecture.BuildDiagram(store, opts)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	doc := architecture.DocumentFromDiagramResult(repoID, result)
	respondJSON(w, http.StatusOK, doc)
}

// handleGetDiagramDocument returns the best available diagram document for a repo.
func (s *Server) handleGetDiagramDocument(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	diagStore.mu.RLock()
	for _, sk := range []architecture.SourceKind{
		architecture.SourceUserEdited,
		architecture.SourceImportedMermaid,
		architecture.SourceAIGenerated,
		architecture.SourceDeterministic,
	} {
		if doc, ok := diagStore.docs[diagramKey(repoID, sk)]; ok {
			diagStore.mu.RUnlock()
			respondJSON(w, http.StatusOK, doc)
			return
		}
	}
	diagStore.mu.RUnlock()

	// No stored document — generate deterministic on the fly
	s.handleGetStructuredDiagram(w, r)
}

// handleImportMermaid parses Mermaid input and normalizes it into a DiagramDocument.
func (s *Server) handleImportMermaid(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
		return
	}

	var input struct {
		Mermaid string `json:"mermaid"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if input.Mermaid == "" {
		http.Error(w, `{"error":"mermaid field is required"}`, http.StatusBadRequest)
		return
	}

	doc, err := architecture.ParseMermaid(repoID, input.Mermaid)
	if err != nil {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
			"error":  "failed to parse Mermaid",
			"detail": err.Error(),
		})
		return
	}

	mode := architecture.ImportImprove
	switch input.Mode {
	case "preserve":
		mode = architecture.ImportPreserve
	case "simplify":
		mode = architecture.ImportSimplify
	}

	normResult := architecture.Normalize(doc, mode)
	generatedMermaid := doc.GenerateMermaid()

	diagStore.mu.Lock()
	diagStore.docs[diagramKey(repoID, architecture.SourceImportedMermaid)] = doc
	diagStore.mu.Unlock()

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"document":      doc,
		"mermaid":       generatedMermaid,
		"normalization": normResult,
	})
}

// handleExportDiagramMermaid generates and returns Mermaid source.
func (s *Server) handleExportDiagramMermaid(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	doc := getDiagramDoc(repoID)
	if doc == nil {
		http.Error(w, `{"error":"no diagram found"}`, http.StatusNotFound)
		return
	}

	mermaid := doc.GenerateMermaid()
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename=architecture.mmd")
	w.Write([]byte(mermaid))
}

// handleExportDiagramJSON exports the structured document as JSON.
func (s *Server) handleExportDiagramJSON(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	doc := getDiagramDoc(repoID)
	if doc == nil {
		http.Error(w, `{"error":"no diagram found"}`, http.StatusNotFound)
		return
	}

	jsonStr, err := doc.ToJSON()
	if err != nil {
		http.Error(w, `{"error":"failed to serialize"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=architecture.json")
	w.Write([]byte(jsonStr))
}

func getDiagramDoc(repoID string) *architecture.DiagramDocument {
	diagStore.mu.RLock()
	defer diagStore.mu.RUnlock()

	for _, sk := range []architecture.SourceKind{
		architecture.SourceUserEdited,
		architecture.SourceImportedMermaid,
		architecture.SourceAIGenerated,
		architecture.SourceDeterministic,
	} {
		if doc, ok := diagStore.docs[diagramKey(repoID, sk)]; ok {
			return doc
		}
	}
	return nil
}

func respondJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
