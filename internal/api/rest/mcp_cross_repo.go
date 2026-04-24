// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"sort"

	"github.com/sourcebridge/sourcebridge/internal/capabilities"
)

// Phase 3.4 — cross-repo impact tool.
//
// Gated by the cross_repo_impact capability (enterprise-only). On OSS
// editions the tool is hidden from tools/list by the capability-
// registry filter, and any direct invocation returns
// CAPABILITY_DISABLED via the structured error envelope.
//
// The tool reads the existing cross-repo federation primitives the
// graph store already exposes — GetCrossRepoRefs for refs originating
// from a symbol in the given repo, and GetSymbolCrossRepoRefs when
// the caller specified a specific symbol. No new persistence work.

func (h *mcpHandler) crossRepoToolDef() mcpToolDefinition {
	return mcpToolDefinition{
		Name:        "get_cross_repo_impact",
		Description: "Enterprise-only: return cross-repository references emanating from the given repository (or specific symbol), showing how a change in one repo affects downstream repos linked via the federation graph.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"repository_id": map[string]interface{}{"type": "string", "description": "Source repository"},
				"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional — filter to refs originating from this specific symbol"},
				"ref_type":      map[string]interface{}{"type": "string", "description": "Optional filter by ref type (e.g. api_call, import_use)"},
				"limit":         map[string]interface{}{"type": "integer", "description": "Max refs to return (default 50, cap 500)"},
			},
			"required": []string{"repository_id"},
		},
	}
}

type crossRepoRefResult struct {
	SourceSymbolID string `json:"source_symbol_id,omitempty"`
	TargetRepoID   string `json:"target_repo_id"`
	TargetSymbol   string `json:"target_symbol,omitempty"`
	RefType        string `json:"ref_type,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
}

type crossRepoImpactResult struct {
	RepositoryID string                `json:"repository_id"`
	Refs         []crossRepoRefResult  `json:"refs"`
	Total        int                   `json:"total"`
	Message      string                `json:"message,omitempty"`
}

func (h *mcpHandler) callGetCrossRepoImpact(session *mcpSession, args json.RawMessage) (interface{}, error) {
	// Capability gate — refuses cleanly on OSS even if something
	// manages to invoke the tool without going through tools/list.
	if !capabilities.IsAvailable("cross_repo_impact", h.edition) {
		return nil, errCapabilityDisabled("cross_repo_impact")
	}

	var params struct {
		RepositoryID string `json:"repository_id"`
		SymbolID     string `json:"symbol_id"`
		RefType      string `json:"ref_type"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	var refType *string
	if params.RefType != "" {
		refType = &params.RefType
	}

	var out []crossRepoRefResult
	if params.SymbolID != "" {
		refs, err := h.store.GetSymbolCrossRepoRefs(params.SymbolID)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if refType != nil && r.RefType != *refType {
				continue
			}
			out = append(out, crossRepoRefResult{
				SourceSymbolID: r.SourceSymbolID,
				TargetRepoID:   r.TargetRepoID,
				TargetSymbol:   r.TargetSymbolID,
				RefType:        r.RefType,
				Confidence:     r.Confidence,
			})
		}
	} else {
		refs, err := h.store.GetCrossRepoRefs(params.RepositoryID, refType, limit)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			out = append(out, crossRepoRefResult{
				SourceSymbolID: r.SourceSymbolID,
				TargetRepoID:   r.TargetRepoID,
				TargetSymbol:   r.TargetSymbolID,
				RefType:        r.RefType,
				Confidence:     r.Confidence,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].TargetRepoID != out[j].TargetRepoID {
			return out[i].TargetRepoID < out[j].TargetRepoID
		}
		return out[i].TargetSymbol < out[j].TargetSymbol
	})

	total := len(out)
	if total > limit {
		out = out[:limit]
	}

	result := crossRepoImpactResult{
		RepositoryID: params.RepositoryID,
		Refs:         out,
		Total:        total,
	}
	if total == 0 {
		result.Message = "No cross-repository references found. Configure repo dependencies via the enterprise admin panel or /api/v1/enterprise/dependencies."
	}
	return result, nil
}
