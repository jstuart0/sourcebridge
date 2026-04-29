// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package maskutil provides shared helpers for safely surfacing secret
// values in responses without leaking them. The Token function returns
// a "first-4...last-4" preview suitable for confirming "yes, this key
// is the one I expect" without revealing the secret. Used by the REST
// admin handlers (git config, LLM config) and the GraphQL per-repo
// LLM override resolver.
//
// History: extracted from internal/api/rest/git_config.go in slice 2 of
// plan 2026-04-29-workspace-llm-source-of-truth-r2.md so the GraphQL
// surface for the per-repo LLM override can reuse the same masking
// without duplicating the logic.
package maskutil

// Token returns a masked preview of a secret. Tokens of 8 or fewer
// characters render as "****" since fewer characters means a preview
// would leak too much. Longer tokens render as the first 4 + "..." +
// the last 4.
func Token(token string) string {
	if len(token) <= 8 {
		return "****"
	}
	return token[:4] + "..." + token[len(token)-4:]
}
