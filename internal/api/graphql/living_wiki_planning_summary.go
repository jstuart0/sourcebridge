// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// GQL-5: buildPlanningSummary has moved to internal/livingwiki/coldstart/runner.go.
// This shim delegates to the canonical implementation so existing graphql-package
// callers (living_wiki_plan_preview.go, schema.resolvers.go, and tests) continue
// to compile without modification.

package graphql

import "github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"

// buildPlanningSummary is a package-private shim delegating to coldstart.
// See coldstart.buildPlanningSummary (package-private) — accessed via this
// package-level wrapper because the coldstart implementation is unexported.
func buildPlanningSummary(
	mode string,
	total int,
	clusterPages int,
	topLevelDirPages int,
	repoWidePages int,
	capSource string,
	capValue int,
	preCap int,
) string {
	return coldstart.BuildPlanningSummary(mode, total, clusterPages, topLevelDirPages, repoWidePages, capSource, capValue, preCap)
}
