// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package-internal helpers for stamping outgoing gRPC metadata onto
// contexts headed to the worker. Slice 2 of the workspace-LLM-source-
// of-truth plan removed the legacy withModelMetadata / withJobMetadata
// pair — every LLM-bearing RPC now flows through *llmcall.Caller, which
// resolves and attaches the runtime LLM config (provider / api-key /
// model) on its own. The remaining helpers in this file (llmJobMetadata,
// withCliffNotesRenderMetadata) attach orthogonal metadata that the
// resolver does not own (per-job tracing IDs, render-only flags).
package graphql

import (
	"context"
	"encoding/json"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// llmJobMetadata builds the llmcall.JobMetadata from an llm.Runtime plus
// artifact ids. Callers pass this to the *WithJob variants of the Caller
// methods so per-job tracing headers continue to flow to the worker.
func llmJobMetadata(rt llm.Runtime, artifactID, jobType string) llmcall.JobMetadata {
	jm := llmcall.JobMetadata{
		ArtifactID: artifactID,
		JobType:    jobType,
	}
	if rt != nil {
		jm.JobID = rt.JobID()
	}
	return jm
}

// llmJobMetadataWithProgress is the same as llmJobMetadata but also
// attaches the supplied OnProgress handler so streaming events drive
// real-progress UI updates (CA-122 Phase 6/7). Pass the handler from
// streamProgressDriver.OnProgress(); call driver.Close() AFTER the
// streaming RPC returns and BEFORE writing any terminal artifact
// status (codex r1b M5 driver-drain rule).
func llmJobMetadataWithProgress(rt llm.Runtime, artifactID, jobType string, onProgress func(worker.KnowledgeStreamEvent)) llmcall.JobMetadata {
	jm := llmJobMetadata(rt, artifactID, jobType)
	jm.OnProgress = onProgress
	return jm
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
