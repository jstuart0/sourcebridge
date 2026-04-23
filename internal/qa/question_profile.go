// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

// QuestionProfile extends the keyword-based QuestionKind with
// evidence-kind hints and LLM-extracted candidates. The loop uses
// these to pre-populate the seed context so the model starts with
// higher-quality hypotheses instead of burning tool calls on
// exploratory retrieval.
//
// Profile is produced by ProfileQuestion — either the keyword
// profiler (always-on) or the LLM profiler (flag-gated). Both
// return a well-formed profile; the LLM profiler just provides
// tighter Evidence hints and non-empty candidate lists.
type QuestionProfile struct {
	Kind          QuestionKind
	EvidenceHints EvidenceKindHints
}

// EvidenceKindHints tells the seed-context builder which kinds of
// evidence to pre-populate before handing control to the agentic
// loop. A true flag means "this question is likely to benefit from
// that evidence kind; put some upfront so the first turn can see
// it".
//
// Symbol/File/Topic candidates are the LLM's best guesses at what
// the question is about. They're advisory — the loop may still
// use search_evidence / read_file freely — but they shrink the
// search space and seed the model's first hypothesis.
type EvidenceKindHints struct {
	NeedsCallGraph    bool
	NeedsRequirements bool
	NeedsTests        bool
	NeedsSummaries    bool
	// Advisory candidate lists. Empty on the keyword path.
	SymbolCandidates []string
	FileCandidates   []string
	TopicTerms       []string
}

// DefaultProfile returns the keyword-only profile — run the
// existing keyword classifier, leave the Evidence hints derived
// from class defaults. This is the fail-open path when the LLM
// profiler is unavailable or disabled.
func DefaultProfile(question string) QuestionProfile {
	kind := ClassifyQuestion(question)
	return QuestionProfile{
		Kind:          kind,
		EvidenceHints: defaultHintsForKind(kind),
	}
}

// defaultHintsForKind is the best-effort mapping from question
// class to likely evidence kinds, used when we don't have an LLM
// profile. Tuned against Phase-3 benchmark patterns: architecture
// and execution_flow benefit from graph; cross_cutting and
// ownership benefit from summaries; behavior benefits from tests.
func defaultHintsForKind(kind QuestionKind) EvidenceKindHints {
	switch kind {
	case KindArchitecture:
		return EvidenceKindHints{
			NeedsCallGraph: true,
			NeedsSummaries: true,
		}
	case KindExecutionFlow:
		return EvidenceKindHints{
			NeedsCallGraph: true,
		}
	case KindBehavior:
		return EvidenceKindHints{
			NeedsTests:     true,
			NeedsCallGraph: true,
		}
	case KindRequirementCoverage:
		return EvidenceKindHints{
			NeedsRequirements: true,
		}
	case KindOwnership:
		return EvidenceKindHints{
			NeedsSummaries: true,
		}
	case KindDataModel:
		return EvidenceKindHints{
			NeedsSummaries: true,
		}
	case KindRiskReview:
		return EvidenceKindHints{
			NeedsCallGraph: true,
			NeedsTests:     true,
		}
	}
	return EvidenceKindHints{}
}
