// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sourcebridge/sourcebridge/internal/architecture"
)

type diagramDocumentPersistence interface {
	StoreDiagramDocument(doc *architecture.DiagramDocument) error
	GetDiagramDocument(repoID string, sourceKinds ...architecture.SourceKind) *architecture.DiagramDocument
	DeleteDiagramDocument(repoID string, sourceKind architecture.SourceKind) error
}

// diagramDocumentStore is an in-memory fallback for diagram documents.
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

func (s *diagramDocumentStore) StoreDiagramDocument(doc *architecture.DiagramDocument) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := *doc
	s.docs[diagramKey(doc.RepositoryID, doc.SourceKind)] = &cloned
	return nil
}

func (s *diagramDocumentStore) GetDiagramDocument(repoID string, sourceKinds ...architecture.SourceKind) *architecture.DiagramDocument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sourceKind := range sourceKinds {
		if doc, ok := s.docs[diagramKey(repoID, sourceKind)]; ok {
			cloned := *doc
			return &cloned
		}
	}
	return nil
}

func (s *diagramDocumentStore) DeleteDiagramDocument(repoID string, sourceKind architecture.SourceKind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, diagramKey(repoID, sourceKind))
	return nil
}

func (s *Server) diagramStoreForRequest(r *http.Request) diagramDocumentPersistence {
	if store, ok := s.store.(diagramDocumentPersistence); ok {
		return store
	}
	return diagStore
}

// handleGetStructuredDiagram returns the deterministic diagram as a DiagramDocument.
func (s *Server) handleGetStructuredDiagram(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	store := s.diagramStoreForRequest(r)

	// Check for a user-edited version first
	if doc := store.GetDiagramDocument(repoID, architecture.SourceUserEdited); doc != nil {
		respondJSON(w, http.StatusOK, doc)
		return
	}

	depth := 1
	if raw := r.URL.Query().Get("depth"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 3 {
			depth = parsed
		}
	}
	maxNodes := 30
	if raw := r.URL.Query().Get("max_nodes"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 100 {
			maxNodes = parsed
		}
	}

	doc, err := s.buildDeterministicDiagramDoc(r, repoID, depth, maxNodes)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, doc)
}

// handleGetDiagramDocument returns the best available diagram document for a repo.
func (s *Server) handleGetDiagramDocument(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	store := s.diagramStoreForRequest(r)
	if doc := store.GetDiagramDocument(repoID,
		architecture.SourceUserEdited,
		architecture.SourceImportedMermaid,
		architecture.SourceAIGenerated,
		architecture.SourceDeterministic,
	); doc != nil {
		respondJSON(w, http.StatusOK, doc)
		return
	}

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

	if err := s.diagramStoreForRequest(r).StoreDiagramDocument(doc); err != nil {
		http.Error(w, `{"error":"failed to store diagram"}`, http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"document":      doc,
		"mermaid":       generatedMermaid,
		"normalization": normResult,
	})
}

// handleExportDiagramMermaid generates and returns Mermaid source.
func (s *Server) handleExportDiagramMermaid(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	doc := s.diagramStoreForRequest(r).GetDiagramDocument(repoID,
		architecture.SourceUserEdited,
		architecture.SourceImportedMermaid,
		architecture.SourceAIGenerated,
		architecture.SourceDeterministic,
	)
	if doc == nil {
		var err error
		doc, err = s.buildDeterministicDiagramDoc(r, repoID, 1, 30)
		if err != nil {
			http.Error(w, `{"error":"no diagram available"}`, http.StatusNotFound)
			return
		}
	}

	mermaid := doc.GenerateMermaid()
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename=architecture.mmd")
	w.Write([]byte(mermaid))
}

// handleExportDiagramJSON exports the structured document as JSON.
func (s *Server) handleExportDiagramJSON(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	doc := s.diagramStoreForRequest(r).GetDiagramDocument(repoID,
		architecture.SourceUserEdited,
		architecture.SourceImportedMermaid,
		architecture.SourceAIGenerated,
		architecture.SourceDeterministic,
	)
	if doc == nil {
		var err error
		doc, err = s.buildDeterministicDiagramDoc(r, repoID, 1, 30)
		if err != nil {
			http.Error(w, `{"error":"no diagram available"}`, http.StatusNotFound)
			return
		}
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

func (s *Server) handlePutDiagramDocument(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}
	if s.getStore(r).GetRepository(repoID) == nil {
		http.Error(w, `{"error":"repository not found"}`, http.StatusNotFound)
		return
	}

	var doc architecture.DiagramDocument
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&doc); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	existing := s.diagramStoreForRequest(r).GetDiagramDocument(repoID, architecture.SourceUserEdited)
	if existing != nil && !existing.CreatedAt.IsZero() {
		doc.CreatedAt = existing.CreatedAt
	} else if doc.CreatedAt.IsZero() {
		doc.CreatedAt = now
	}
	doc.RepositoryID = repoID
	doc.SourceKind = architecture.SourceUserEdited
	doc.UpdatedAt = now
	if doc.ID == "" {
		doc.ID = "user-" + repoID
	}

	if err := s.diagramStoreForRequest(r).StoreDiagramDocument(&doc); err != nil {
		http.Error(w, `{"error":"failed to store diagram"}`, http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"document": &doc,
	})
}

func (s *Server) handleDeleteDiagramDocument(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}
	if err := s.diagramStoreForRequest(r).DeleteDiagramDocument(repoID, architecture.SourceUserEdited); err != nil {
		http.Error(w, `{"error":"failed to delete diagram"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) buildDeterministicDiagramDoc(r *http.Request, repoID string, depth int, maxNodes int) (*architecture.DiagramDocument, error) {
	store := s.getStore(r)
	result, err := architecture.BuildDiagram(store, architecture.DiagramOpts{
		RepoID:      repoID,
		Level:       "MODULE",
		ModuleDepth: depth,
		MaxNodes:    maxNodes,
	})
	if err != nil {
		return nil, err
	}
	return architecture.DocumentFromDiagramResult(repoID, result), nil
}

func respondJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
