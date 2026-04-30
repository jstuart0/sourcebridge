// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

var indexCmd = &cobra.Command{
	Use:   "index [repo-path]",
	Short: "Index a repository for code analysis",
	Long:  "Parse and index a Git repository, extracting functions, classes, modules, and call graphs.",
	Args:  cobra.ExactArgs(1),
	RunE:  runIndex,
}

var (
	indexJSON  bool
	indexRetry bool
)

func init() {
	indexCmd.Flags().BoolVar(&indexJSON, "json", false, "Output results as JSON")
	indexCmd.Flags().BoolVar(&indexRetry, "retry", false, "Retry previously failed indexing")
}

// formatSymbolCount formats a symbol count with thousands separators.
func formatSymbolCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	rem := len(s) % 3
	if rem > 0 {
		out = append(out, s[:rem]...)
	}
	for i := rem; i < len(s); i += 3 {
		if i > 0 || rem > 0 {
			out = append(out, ',')
		}
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}

func runIndex(cmd *cobra.Command, args []string) error {
	repoPath := args[0]

	progressFn := func(evt indexer.ProgressEvent) {
		if evt.Phase == "complete" {
			fmt.Fprintf(os.Stderr, "\rIndexing complete: %d/%d files\n", evt.Current, evt.Total)
		} else if evt.File != "" {
			fmt.Fprintf(os.Stderr, "\r[%d/%d] %s: %s", evt.Current, evt.Total, evt.Phase, evt.File)
		} else {
			fmt.Fprintf(os.Stderr, "\r%s... %.0f%%", evt.Description, evt.Progress*100)
		}
	}

	idx := indexer.NewIndexer(progressFn)
	// `sourcebridge index` is the operator-driven first-touch CLI for
	// indexing a repo; per the latent-full-reindex audit (plan v5),
	// every IndexRepository caller must pass a typed reason.
	result, err := idx.IndexRepository(context.Background(), repoPath, indexer.ReasonInitialOnboard)
	if err != nil {
		return fmt.Errorf("indexing failed: %w", err)
	}

	// Store results in graph
	store := graph.NewStore()
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		return fmt.Errorf("storing results: %w", err)
	}

	if indexJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"repository": repo,
			"stats": map[string]int{
				"files":     result.TotalFiles,
				"symbols":   result.TotalSymbols,
				"relations": result.TotalRelations,
				"modules":   len(result.Modules),
			},
		})
	}

	fmt.Fprintf(os.Stdout, "\nRepository: %s\n", result.RepoName)
	fmt.Fprintf(os.Stdout, "Files:      %d\n", result.TotalFiles)
	fmt.Fprintf(os.Stdout, "Symbols:    %d\n", result.TotalSymbols)
	fmt.Fprintf(os.Stdout, "Modules:    %d\n", len(result.Modules))
	fmt.Fprintf(os.Stdout, "Relations:  %d\n", result.TotalRelations)
	if len(result.Errors) > 0 {
		fmt.Fprintf(os.Stdout, "Errors:     %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
	}

	if repo.ID != "" {
		fmt.Fprintf(os.Stdout, "\nIndexed %s symbols. Use with Claude Code:\n  sourcebridge setup claude --repo-id %s\n",
			formatSymbolCount(result.TotalSymbols), repo.ID)
	} else {
		fmt.Fprintf(os.Stdout, "\nIndexed %s symbols.\n", formatSymbolCount(result.TotalSymbols))
	}

	return nil
}
