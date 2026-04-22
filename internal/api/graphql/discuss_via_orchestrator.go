// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"

	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// dispatchDiscussThroughOrchestrator routes a DiscussCodeInput through
// the QA orchestrator and flattens the resulting AskResult to the
// legacy DiscussionResult wire shape. Used when r.QA is non-nil
// (which happens when QAConfig.ServerSideEnabled is true — see
// qaResolverOrchestrator in internal/api/rest/router.go).
//
// The goal is zero observable change for existing callers. Every
// field on DiscussionResult is populated from AskResult in a way
// that mirrors the legacy resolver's output, and structured
// references are lossy-flattened to []string via
// qa.FlattenReferencesToStrings so clients that expect strings
// continue to work.
//
// Clients that want structured references should use the new `ask`
// mutation instead — it surfaces AskResult unchanged.
func (r *mutationResolver) dispatchDiscussThroughOrchestrator(ctx context.Context, input DiscussCodeInput) (*DiscussionResult, error) {
	askInput := qa.AskInput{
		RepositoryID:  input.RepositoryID,
		Question:      input.Question,
		Mode:          qa.ModeDeep, // discussCode has always been "grounded QA"; deep is the right default
		PriorMessages: input.ConversationHistory,
	}
	if input.FilePath != nil {
		askInput.FilePath = *input.FilePath
	}
	if input.Code != nil {
		askInput.Code = *input.Code
	}
	if input.Language != nil {
		askInput.Language = input.Language.String()
	}
	if input.ArtifactID != nil {
		askInput.ArtifactID = *input.ArtifactID
	}
	if input.SymbolID != nil {
		askInput.SymbolID = *input.SymbolID
	}
	if input.RequirementID != nil {
		askInput.RequirementID = *input.RequirementID
	}

	res, err := r.QA.Ask(ctx, askInput)
	if err != nil {
		return nil, err
	}

	out := &DiscussionResult{
		Answer:              res.Answer,
		References:          qa.FlattenReferencesToStrings(res.References),
		RelatedRequirements: res.RelatedRequirements,
	}
	if out.References == nil {
		out.References = []string{}
	}
	if out.RelatedRequirements == nil {
		out.RelatedRequirements = []string{}
	}
	if res.Usage.Model != "" {
		m := res.Usage.Model
		out.Model = &m
	}
	if res.Usage.InputTokens > 0 {
		n := res.Usage.InputTokens
		out.InputTokens = &n
	}
	if res.Usage.OutputTokens > 0 {
		n := res.Usage.OutputTokens
		out.OutputTokens = &n
	}
	return out, nil
}
