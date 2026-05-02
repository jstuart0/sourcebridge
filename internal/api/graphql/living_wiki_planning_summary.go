// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// CA-146: planning summary helper for Living Wiki cold-start page-count
// observability. buildPlanningSummary returns a single human-readable string
// that is both logged via slog and surfaced in rt.ReportProgress so operators
// and users see the same rationale.

package graphql

import "fmt"

// buildPlanningSummary returns a human-readable rationale for the planned
// page count. It is the single source of truth for the planning surface —
// used in both the structured log and the user-visible ReportProgress message.
//
// Example outputs:
//
//	"mode=lw_detailed: 6 cluster + 3 repo-wide = 9 pages"
//	"mode=lw_detailed: 6 cluster + 3 repo-wide = 9 pages, capped at 5 from MaxPagesPerJob"
//	"mode=lw_detailed: 6 cluster + 3 repo-wide = 9 pages, capped at 4 from pageCountOverride"
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
	var breakdown string
	switch {
	case clusterPages > 0 && topLevelDirPages > 0:
		breakdown = fmt.Sprintf("%d cluster + %d top-level-dir + %d repo-wide",
			clusterPages, topLevelDirPages, repoWidePages)
	case clusterPages > 0:
		breakdown = fmt.Sprintf("%d cluster + %d repo-wide", clusterPages, repoWidePages)
	case topLevelDirPages > 0:
		breakdown = fmt.Sprintf("%d top-level-dir + %d repo-wide", topLevelDirPages, repoWidePages)
	default:
		breakdown = fmt.Sprintf("%d repo-wide", repoWidePages)
	}

	base := fmt.Sprintf("mode=%s: %s = %d pages", mode, breakdown, total)

	switch capSource {
	case "repo_setting":
		return fmt.Sprintf("%s, capped at %d from MaxPagesPerJob (was %d)", base, capValue, preCap)
	case "per_run_override":
		return fmt.Sprintf("%s, capped at %d from pageCountOverride (was %d)", base, capValue, preCap)
	default:
		return base
	}
}
