// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// askInputToQA maps the gqlgen-generated AskInput to qa.AskInput.
// Kept as a pure function so it can be unit-tested without standing up
// a resolver.
func askInputToQA(in AskInput) qa.AskInput {
	mode := qa.Mode(strings.ToLower(orEmpty(in.Mode)))
	out := qa.AskInput{
		RepositoryID:   in.RepositoryID,
		Question:       in.Question,
		Mode:           mode,
		ConversationID: orEmpty(in.ConversationID),
		PriorMessages:  in.PriorMessages,
		FilePath:       orEmpty(in.FilePath),
		Code:           orEmpty(in.Code),
		ArtifactID:     orEmpty(in.ArtifactID),
		SymbolID:       orEmpty(in.SymbolID),
		RequirementID:  orEmpty(in.RequirementID),
	}
	if in.Language != nil {
		out.Language = in.Language.String()
	}
	if in.IncludeDebug != nil {
		out.IncludeDebug = *in.IncludeDebug
	}
	return out
}

// askResultFromQA maps qa.AskResult to the gqlgen-generated AskResult.
func askResultFromQA(r *qa.AskResult) *AskResult {
	if r == nil {
		return &AskResult{}
	}
	out := &AskResult{
		Answer:              r.Answer,
		References:          make([]*AskReference, 0, len(r.References)),
		RelatedRequirements: r.RelatedRequirements,
		Diagnostics:         askDiagnosticsFromQA(r.Diagnostics),
		Usage:               askUsageFromQA(r.Usage),
	}
	for _, ref := range r.References {
		out.References = append(out.References, askReferenceFromQA(ref))
	}
	if r.Debug != nil {
		out.Debug = askDebugFromQA(r.Debug)
	}
	return out
}

func askReferenceFromQA(r qa.AskReference) *AskReference {
	title := r.Title
	ref := &AskReference{
		Kind:  string(r.Kind),
		Title: optString(title),
	}
	if r.Symbol != nil {
		ref.Symbol = &AskSymbolRef{
			SymbolID:      r.Symbol.SymbolID,
			QualifiedName: r.Symbol.QualifiedName,
			FilePath:      optString(r.Symbol.FilePath),
			StartLine:     optInt(r.Symbol.StartLine),
			EndLine:       optInt(r.Symbol.EndLine),
			Language:      optString(r.Symbol.Language),
		}
	}
	if r.FileRange != nil {
		ref.FileRange = &AskFileRangeRef{
			FilePath:  r.FileRange.FilePath,
			StartLine: optInt(r.FileRange.StartLine),
			EndLine:   optInt(r.FileRange.EndLine),
			Snippet:   optString(r.FileRange.Snippet),
		}
	}
	if r.Requirement != nil {
		ref.Requirement = &AskRequirementRef{
			ExternalID: r.Requirement.ExternalID,
			Title:      optString(r.Requirement.Title),
			FilePath:   optString(r.Requirement.FilePath),
		}
	}
	if r.UnderstandingSection != nil {
		ref.UnderstandingSection = &AskUnderstandingSectionRef{
			ArtifactID: optString(r.UnderstandingSection.ArtifactID),
			SectionID:  optString(r.UnderstandingSection.SectionID),
			Headline:   optString(r.UnderstandingSection.Headline),
			Kind:       optString(r.UnderstandingSection.Kind),
			ActionURL:  optString(r.UnderstandingSection.ActionURL),
		}
	}
	if r.CrossRepo != nil {
		ref.CrossRepo = &AskCrossRepoRef{
			RepositoryID: r.CrossRepo.RepositoryID,
			FilePath:     optString(r.CrossRepo.FilePath),
			Note:         optString(r.CrossRepo.Note),
		}
	}
	return ref
}

func askDiagnosticsFromQA(d qa.AskDiagnostics) *AskDiagnostics {
	out := &AskDiagnostics{
		QuestionType:          optString(d.QuestionType),
		UnderstandingStage:    optString(d.UnderstandingStage),
		TreeStatus:            optString(d.TreeStatus),
		UnderstandingRevision: optString(d.UnderstandingRevision),
		UnderstandingUsed:     optBool(d.UnderstandingUsed),
		GraphExpansionUsed:    optBool(d.GraphExpansionUsed),
		FilesConsidered:       d.FilesConsidered,
		FilesUsed:             d.FilesUsed,
		FallbackUsed:          optString(d.FallbackUsed),
		ModelUsed:             optString(d.ModelUsed),
		Mode:                  optString(d.Mode),
	}
	if len(d.StageTimings) > 0 {
		// Stable order by stage name so GraphQL responses diff cleanly.
		keys := make([]string, 0, len(d.StageTimings))
		for k := range d.StageTimings {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			out.StageTimingsMs = append(out.StageTimingsMs, &AskStageTiming{
				Stage:      k,
				DurationMs: int(d.StageTimings[k]),
			})
		}
	}
	return out
}

func askUsageFromQA(u qa.AskUsage) *AskUsage {
	return &AskUsage{
		Model:        optString(u.Model),
		InputTokens:  optInt(u.InputTokens),
		OutputTokens: optInt(u.OutputTokens),
	}
}

func askDebugFromQA(d *qa.AskDebug) *AskDebug {
	if d == nil {
		return nil
	}
	out := &AskDebug{
		Prompt:          optString(d.Prompt),
		ContextMarkdown: optString(d.ContextMarkdown),
	}
	for _, c := range d.Candidates {
		out.Candidates = append(out.Candidates, &AskDebugCandidate{
			Source: c.Source,
			ID:     optString(c.ID),
			Score:  optFloat(c.Score),
			Reason: optString(c.Reason),
		})
	}
	return out
}

// --- small optional-pointer helpers ----------------------------------

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func optInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}
func optBool(b bool) *bool {
	if !b {
		return nil
	}
	return &b
}
func optFloat(f float64) *float64 {
	if f == 0 {
		return nil
	}
	return &f
}

// sortStrings avoids importing sort in this adapter file just for a
// one-call need, keeping the file focused on the mapping layer.
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
