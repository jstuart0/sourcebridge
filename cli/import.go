// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/requirements"
)

var importCmd = &cobra.Command{
	Use:   "import [file]",
	Short: "Import requirements from a markdown or CSV file",
	Long:  "Parse and import requirements from structured documents into a repository's graph.",
	Args:  cobra.ExactArgs(1),
	RunE:  runImport,
}

var (
	importRepoID string
	importFormat string
	importJSON   bool
)

func init() {
	importCmd.Flags().StringVar(&importRepoID, "repo-id", "", "Repository ID to associate requirements with")
	importCmd.Flags().StringVar(&importFormat, "format", "", "File format (markdown, csv). Auto-detected from extension if not specified.")
	importCmd.Flags().BoolVar(&importJSON, "json", false, "Output results as JSON")
}

func runImport(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	// Auto-detect format from extension
	format := importFormat
	if format == "" {
		ext := strings.ToLower(filepath.Ext(filePath))
		switch ext {
		case ".md", ".markdown":
			format = "markdown"
		case ".csv":
			format = "csv"
		default:
			return fmt.Errorf("cannot detect format from extension %q; use --format flag", ext)
		}
	}

	var parsed *requirements.ParseResult
	switch format {
	case "markdown", "md":
		parsed = requirements.ParseMarkdown(string(content))
	case "csv":
		parsed = requirements.ParseCSV(string(content), nil)
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}

	// Store results
	store := graph.NewStore()

	// Create a placeholder repo if no repo-id specified
	repoID := importRepoID
	if repoID == "" {
		repoID = "default"
	}

	source := filepath.Base(filePath)
	var storedReqs []*graph.StoredRequirement
	for _, req := range parsed.Requirements {
		storedReqs = append(storedReqs, &graph.StoredRequirement{
			ExternalID:         req.ExternalID,
			Title:              req.Title,
			Description:        req.Description,
			Source:             source,
			Priority:           req.Priority,
			AcceptanceCriteria: req.AcceptanceCriteria,
			Tags:               req.Tags,
		})
	}

	imported := store.StoreRequirements(repoID, storedReqs)

	if importJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"imported": imported,
			"skipped":  0,
			"warnings": parsed.Warnings,
			"requirements": func() []map[string]interface{} {
				var out []map[string]interface{}
				for _, r := range parsed.Requirements {
					out = append(out, map[string]interface{}{
						"external_id": r.ExternalID,
						"title":       r.Title,
						"priority":    r.Priority,
						"criteria":    len(r.AcceptanceCriteria),
					})
				}
				return out
			}(),
		})
	}

	fmt.Fprintf(os.Stdout, "\nImported: %d requirements from %s\n", imported, filepath.Base(filePath))
	fmt.Fprintf(os.Stdout, "Format:   %s\n", format)
	if len(parsed.Warnings) > 0 {
		fmt.Fprintf(os.Stdout, "Warnings: %d\n", len(parsed.Warnings))
		for _, w := range parsed.Warnings {
			fmt.Fprintf(os.Stderr, "  - %s\n", w)
		}
	}

	fmt.Fprintf(os.Stdout, "\nRequirements:\n")
	for _, r := range parsed.Requirements {
		fmt.Fprintf(os.Stdout, "  %s: %s", r.ExternalID, r.Title)
		if r.Priority != "" {
			fmt.Fprintf(os.Stdout, " [%s]", r.Priority)
		}
		fmt.Fprintf(os.Stdout, " (%d criteria)\n", len(r.AcceptanceCriteria))
	}

	return nil
}
