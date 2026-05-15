// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"fmt"

	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// dispatchDiscussThroughOrchestrator routes a DiscussCodeInput through
// the QA orchestrator and flattens the resulting AskResult to the
// legacy DiscussionResult wire shape. Used when r.Deps.QA is non-nil
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
	// SEC-5: verify repo access before passing the caller-controlled
	// repositoryID to the QA orchestrator.  Without this check, the
	// orchestrator's base-store dependencies bypass the tenant-filtered
	// store the resolver normally uses.
	if err := r.checkRepoAccessGraphQL(ctx, input.RepositoryID); err != nil {
		return nil, err
	}
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

	res, err := r.Deps.QA.Ask(ctx, askInput)
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

	// Fix C: eliminate the silent-empty-answer path. When synthesis
	// produced no text, surface an explicit message so callers always
	// receive either a real answer or an explanation of why one wasn't
	// produced.
	applyEmptyAnswerGuard(res, out)

	return out, nil
}

// applyEmptyAnswerGuard applies Fix C (CA-324) to out in-place:
// when Answer is empty it substitutes a human-readable explanation
// sourced from either the fallback reason (C1) or a generic message (C2).
// Extracted so the guard can be unit-tested independently of the full
// orchestrator round-trip (CA-392 / T-M1).
func applyEmptyAnswerGuard(res *qa.AskResult, out *DiscussionResult) {
	if out.Answer != "" {
		return
	}
	if res.Diagnostics.FallbackUsed != "" {
		// C1: pipeline signalled a degraded path (e.g. worker_unavailable,
		// understanding_partial, synthesis_failed). Surface the reason.
		out.Answer = fmt.Sprintf(
			"Discussion synthesis returned no answer (reason: %s). %d source(s) gathered — see references list for context.",
			res.Diagnostics.FallbackUsed, len(out.References),
		)
	} else {
		// C2: truly silent path — worker returned an empty answer with no
		// diagnostic signal. Surface a generic explanation.
		out.Answer = fmt.Sprintf(
			"Discussion synthesis completed but returned an empty answer. %d source(s) gathered — see references list for context.",
			len(out.References),
		)
	}
}
