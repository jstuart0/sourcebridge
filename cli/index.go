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
	result, err := idx.IndexRepository(context.Background(), repoPath)
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

	return nil
}
