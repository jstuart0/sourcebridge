// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// withModelMetadata enriches a context with the API's effective LLM
// configuration for a single worker call. The API owns the DB-backed
// LLM settings, so the worker should not rely on deployment env vars
// as the runtime source of truth.
func (r *Resolver) withModelMetadata(ctx context.Context, operationGroup string) context.Context {
	return r.withJobMetadata(ctx, operationGroup, nil, "", "", "")
}

func (r *Resolver) withJobMetadata(
	ctx context.Context,
	operationGroup string,
	rt llm.Runtime,
	repoID, artifactID, jobType string,
) context.Context {
	if r.Config == nil {
		return ctx
	}

	model := r.Config.LLM.ModelForOperation(operationGroup)
	if operationGroup == "knowledge" && jobType == "architecture_diagram" && r.Config.LLM.ArchitectureDiagramModel != "" {
		model = r.Config.LLM.ArchitectureDiagramModel
	}
	pairs := []string{
		"x-sb-llm-provider", r.Config.LLM.Provider,
		"x-sb-llm-base-url", r.Config.LLM.BaseURL,
		"x-sb-llm-api-key", r.Config.LLM.APIKey,
		"x-sb-llm-draft-model", r.Config.LLM.DraftModel,
		"x-sb-operation", operationGroup,
	}
	if r.Config.LLM.TimeoutSecs > 0 {
		pairs = append(pairs, "x-sb-llm-timeout-seconds", strconv.Itoa(r.Config.LLM.TimeoutSecs))
	}
	if model != "" {
		pairs = append(pairs, "x-sb-model", model)
	}
	if rt != nil && rt.JobID() != "" {
		pairs = append(pairs, "x-sb-job-id", rt.JobID())
	}
	if repoID != "" {
		pairs = append(pairs, "x-sb-repo-id", repoID)
	}
	if artifactID != "" {
		pairs = append(pairs, "x-sb-artifact-id", artifactID)
	}
	if jobType != "" {
		pairs = append(pairs, "x-sb-job-type", jobType)
	}
	pairs = append(pairs, "x-sb-subsystem", "knowledge")
	md := metadata.Pairs(pairs...)
	return metadata.NewOutgoingContext(ctx, md)
}

func withCliffNotesRenderMetadata(
	ctx context.Context,
	renderOnly bool,
	selectedSectionTitles []string,
	understandingDepth string,
	relevanceProfile string,
) context.Context {
	if !renderOnly && len(selectedSectionTitles) == 0 && understandingDepth == "" && relevanceProfile == "" {
		return ctx
	}
	pairs := []string{}
	if renderOnly {
		pairs = append(pairs, "x-sb-cliff-render-only", "true")
	}
	if len(selectedSectionTitles) > 0 {
		if raw, err := json.Marshal(selectedSectionTitles); err == nil {
			pairs = append(pairs, "x-sb-cliff-selected-sections", string(raw))
		}
	}
	if strings.TrimSpace(understandingDepth) != "" {
		pairs = append(pairs, "x-sb-cliff-understanding-depth", strings.TrimSpace(strings.ToLower(understandingDepth)))
	}
	if strings.TrimSpace(relevanceProfile) != "" {
		pairs = append(pairs, "x-sb-cliff-relevance-profile", strings.TrimSpace(strings.ToLower(relevanceProfile)))
	}
	if len(pairs) == 0 {
		return ctx
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	for i := 0; i < len(pairs); i += 2 {
		md.Set(pairs[i], pairs[i+1])
	}
	return metadata.NewOutgoingContext(ctx, md)
}
