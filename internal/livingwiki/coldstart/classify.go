// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package coldstart provides the failure-category classifier and post-job hook
// for living-wiki cold-start and incremental-regen jobs.
//
// The failure taxonomy drives three distinct UX paths in the settings panel
// (R4) and in RepoJobsPopover:
//
//   - [FailureCategoryTransient]     — network blip, rate limit, 5xx.  CTA: Retry.
//   - [FailureCategoryAuth]          — 401/403 from a sink or expired token.
//                                     CTA: "Fix credentials" deep-link to /settings/living-wiki.
//   - [FailureCategoryPartialContent]— some pages excluded by validators; K of N
//                                     pages succeeded. CTA: "Retry excluded pages"
//                                     (sets retryExcludedOnly=true on the next
//                                     enableLivingWikiForRepo call).
package coldstart

import (
	"net/http"
	"strings"
)

// FailureCategory is the three-bucket error taxonomy for living-wiki jobs.
// It is persisted in [livingwiki.LivingWikiJobResult.FailureCategory] and
// surfaced in the GraphQL schema as LivingWikiJobResult.failureCategory.
type FailureCategory string

const (
	// FailureCategoryNone means the job succeeded (or partial-succeeded).
	// Stored when Status == "ok" or "partial".
	FailureCategoryNone FailureCategory = ""

	// FailureCategoryTransient means the failure is likely temporary:
	// network error, 429 rate limit, 5xx from a sink, LLM quota exhausted.
	// The UI shows a "Retry" button.
	FailureCategoryTransient FailureCategory = "transient"

	// FailureCategoryAuth means the job failed because a sink or the source
	// repo returned 401 or 403. Retrying without fixing credentials is useless.
	// The UI shows a "Fix credentials" link to /settings/living-wiki.
	FailureCategoryAuth FailureCategory = "auth"

	// FailureCategoryPartialContent means the orchestrator completed but at
	// least one page was excluded by a quality validator. The job status is
	// "partial" (not "failed"). The UI shows the excluded-pages list and a
	// "Retry excluded pages" CTA.
	FailureCategoryPartialContent FailureCategory = "partial_content"

	// FailureCategorySystemicLLM means the orchestrator's sliding-window
	// soft-failure breaker tripped — many same-category page failures in a
	// row indicate the LLM provider is likely unreachable (DeadlineExceeded
	// from every page, gRPC Unavailable, or persistent empty-content
	// responses). The job status is "partial": pages that completed before
	// the trip ARE persisted, but the user should fix the provider before
	// retrying. The UI should show a distinct CTA ("Check LLM provider
	// config / restart worker") rather than "Retry excluded pages".
	FailureCategorySystemicLLM FailureCategory = "systemic_llm"
)

// authStatusCodes are the HTTP status codes that indicate a credential problem.
var authStatusCodes = map[int]bool{
	http.StatusUnauthorized: true,
	http.StatusForbidden:    true,
}

// ClassifyError maps an error returned by the living-wiki orchestrator or a
// sink HTTP client into one of the three FailureCategory buckets. A nil error
// always returns FailureCategoryNone.
//
// Classification rules (applied in order):
//
//  1. nil error → FailureCategoryNone.
//  2. Errors whose message contains an HTTP status string for 401 or 403 →
//     FailureCategoryAuth.
//  3. Errors whose message mentions network issues, 429, rate limit, 5xx
//     (excluding 401/403), or timeout → FailureCategoryTransient.
//  4. All other errors → FailureCategoryTransient (safe default; callers
//     that know the error is partial should call ClassifyPartial instead).
func ClassifyError(err error) FailureCategory {
	if err == nil {
		return FailureCategoryNone
	}
	msg := strings.ToLower(err.Error())
	return classifyByMessage(msg)
}

// ClassifyHTTPStatus maps an HTTP status code to a FailureCategory.
// Intended for use in sink HTTP clients that have the status code directly.
func ClassifyHTTPStatus(statusCode int) FailureCategory {
	if authStatusCodes[statusCode] {
		return FailureCategoryAuth
	}
	// 429 + any 5xx (excluding 401/403 which are already caught above)
	if statusCode == http.StatusTooManyRequests || statusCode >= 500 {
		return FailureCategoryTransient
	}
	return FailureCategoryNone
}

// classifyByMessage applies heuristic string matching on the lower-cased
// error message. Extracted so ClassifyError and ClassifyHTTPStatus share a
// single ruleset.
func classifyByMessage(msg string) FailureCategory {
	// Auth patterns — check first because "unauthorized" can appear in
	// messages that also mention "retry", which would mis-classify as transient.
	if strings.Contains(msg, "401") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "invalid token") ||
		strings.Contains(msg, "expired token") {
		return FailureCategoryAuth
	}

	// Transient patterns.
	if strings.Contains(msg, "429") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "network") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context deadline") ||
		strings.Contains(msg, "unavailable") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "server error") ||
		strings.Contains(msg, "5xx") ||
		strings.Contains(msg, "llm rate") {
		return FailureCategoryTransient
	}

	// Default: treat as transient so the user can retry.
	return FailureCategoryTransient
}
