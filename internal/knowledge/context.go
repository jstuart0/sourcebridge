// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"fmt"
	"strings"
)

// DiscussionContextFromArtifact renders a knowledge artifact as a
// discussion-context string for injection into LLM prompts.
//
// Format (canonical, GraphQL-shaped):
//
//	Indexed <type> context for <scope>.
//
//	<Section Title 1>:
//	<body 1>
//
//	<Section Title 2>:
//	<body 2>
//
//	…
//
// Sections are joined with "\n\n". Each section body prefers Summary over
// Content; both are TrimSpace'd and capped at 500 bytes. At most 6 sections
// are emitted (the rest are silently dropped).
//
// The previous REST variant used "- %s: %s" with single-newline joining.
// That format is intentionally retired here. Both transports now produce the
// same prompt shape, which the quality-gate canary in context_test.go pins.
// If you need to change this format, update the snapshot canary and add a
// CHANGELOG entry — the LLM reasoning is calibrated against this shape and
// silent drift is a CA-241/CA-329-class regression.
func DiscussionContextFromArtifact(artifact *Artifact) string {
	if artifact == nil || len(artifact.Sections) == 0 {
		return ""
	}
	scopePath := "repository"
	if artifact.Scope != nil {
		scopePath = artifact.Scope.ScopePath
	}
	parts := []string{
		fmt.Sprintf("Indexed %s context for %s.", strings.ToLower(string(artifact.Type)), scopePath),
	}
	for idx, section := range artifact.Sections {
		if idx >= 6 {
			break
		}
		body := section.Summary
		if body == "" {
			body = section.Content
		}
		body = strings.TrimSpace(body)
		if len(body) > 500 {
			body = body[:500] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s:\n%s", section.Title, body))
	}
	return strings.Join(parts, "\n\n")
}
