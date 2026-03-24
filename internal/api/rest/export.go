// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

func (s *Server) handleExportTraceability(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repoId")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}

	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	repo := s.getStore(r).GetRepository(repoID)
	if repo == nil {
		http.Error(w, `{"error":"repository not found"}`, http.StatusNotFound)
		return
	}

	links := s.getStore(r).GetLinksForRepo(repoID)
	reqs, _ := s.getStore(r).GetRequirements(repoID, 0, 0)
	symbols, _ := s.getStore(r).GetSymbols(repoID, nil, nil, 0, 0)

	reqMap := make(map[string]string)
	for _, req := range reqs {
		reqMap[req.ID] = req.ExternalID + ": " + req.Title
	}
	symMap := make(map[string]string)
	for _, sym := range symbols {
		symMap[sym.ID] = sym.QualifiedName
	}

	if format == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=traceability-%s.json", repo.Name))
		type linkExport struct {
			RequirementID   string  `json:"requirement_id"`
			RequirementName string  `json:"requirement_name"`
			SymbolID        string  `json:"symbol_id"`
			SymbolName      string  `json:"symbol_name"`
			Confidence      float64 `json:"confidence"`
			Verified        bool    `json:"verified"`
			Source          string  `json:"source"`
		}
		var exports []linkExport
		for _, l := range links {
			exports = append(exports, linkExport{
				RequirementID:   l.RequirementID,
				RequirementName: reqMap[l.RequirementID],
				SymbolID:        l.SymbolID,
				SymbolName:      symMap[l.SymbolID],
				Confidence:      l.Confidence,
				Verified:        l.Verified,
				Source:          l.Source,
			})
		}
		json.NewEncoder(w).Encode(exports)
		return
	}

	// CSV
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=traceability-%s.csv", repo.Name))
	cw := csv.NewWriter(w)
	cw.Write([]string{"Requirement ID", "Requirement", "Symbol ID", "Symbol", "Confidence", "Verified", "Source"})
	for _, l := range links {
		cw.Write([]string{
			l.RequirementID, reqMap[l.RequirementID],
			l.SymbolID, symMap[l.SymbolID],
			strconv.FormatFloat(l.Confidence, 'f', 2, 64),
			strconv.FormatBool(l.Verified),
			l.Source,
		})
	}
	cw.Flush()
}

func (s *Server) handleExportRequirements(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	repo := s.getStore(r).GetRepository(repoID)
	if repo == nil {
		http.Error(w, `{"error":"repository not found"}`, http.StatusNotFound)
		return
	}

	reqs, _ := s.getStore(r).GetRequirements(repoID, 0, 0)

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=requirements-%s.csv", repo.Name))
	cw := csv.NewWriter(w)
	cw.Write([]string{"External ID", "Title", "Description", "Priority", "Tags", "Source"})
	for _, req := range reqs {
		tags := ""
		for i, t := range req.Tags {
			if i > 0 {
				tags += ";"
			}
			tags += t
		}
		cw.Write([]string{req.ExternalID, req.Title, req.Description, req.Priority, tags, req.Source})
	}
	cw.Flush()
}

func (s *Server) handleExportSymbols(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repoId")
	if repoID == "" {
		http.Error(w, `{"error":"repoId is required"}`, http.StatusBadRequest)
		return
	}

	repo := s.getStore(r).GetRepository(repoID)
	if repo == nil {
		http.Error(w, `{"error":"repository not found"}`, http.StatusNotFound)
		return
	}

	symbols, _ := s.getStore(r).GetSymbols(repoID, nil, nil, 0, 0)

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=symbols-%s.csv", repo.Name))
	cw := csv.NewWriter(w)
	cw.Write([]string{"Name", "Qualified Name", "Kind", "Language", "File Path", "Start Line", "End Line", "Signature"})
	for _, sym := range symbols {
		cw.Write([]string{
			sym.Name, sym.QualifiedName, sym.Kind, sym.Language,
			sym.FilePath, strconv.Itoa(sym.StartLine), strconv.Itoa(sym.EndLine), sym.Signature,
		})
	}
	cw.Flush()
}
