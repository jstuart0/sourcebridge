// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/requirements"
)

var traceReqCmd = &cobra.Command{
	Use:   "trace [requirement-id]",
	Short: "Show code linked to a requirement",
	Long:  "Display bidirectional traceability between requirements and code.",
	Args:  cobra.ExactArgs(1),
	RunE:  runTrace,
}

var (
	traceRepoPath string
	traceReqFile  string
	traceJSON     bool
	traceMinConf  float64
)

func init() {
	traceReqCmd.Flags().StringVar(&traceRepoPath, "repo", ".", "Repository path to index")
	traceReqCmd.Flags().StringVar(&traceReqFile, "requirements", "", "Requirements file (markdown or CSV)")
	traceReqCmd.Flags().BoolVar(&traceJSON, "json", false, "Output results as JSON")
	traceReqCmd.Flags().Float64Var(&traceMinConf, "min-confidence", 0.5, "Minimum confidence threshold")
}

type traceResult struct {
	RequirementID string      `json:"requirement_id"`
	Title         string      `json:"title"`
	Links         []traceLink `json:"links"`
}

type traceLink struct {
	SymbolName string  `json:"symbol_name"`
	FilePath   string  `json:"file_path"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	Rationale  string  `json:"rationale"`
}

func runTrace(cmd *cobra.Command, args []string) error {
	reqID := args[0]

	// Index the repository
	repoPath, err := filepath.Abs(traceRepoPath)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(cmd.Context(), repoPath)
	if err != nil {
		return fmt.Errorf("indexing repository: %w", err)
	}

	store := graph.NewStore()
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		return fmt.Errorf("storing index result: %w", err)
	}

	// Import requirements if file specified
	if traceReqFile != "" {
		content, err := os.ReadFile(traceReqFile)
		if err != nil {
			return fmt.Errorf("reading requirements file: %w", err)
		}

		ext := strings.ToLower(filepath.Ext(traceReqFile))
		var parsed *requirements.ParseResult
		switch ext {
		case ".md", ".markdown":
			parsed = requirements.ParseMarkdown(string(content))
		case ".csv":
			parsed = requirements.ParseCSV(string(content), nil)
		default:
			return fmt.Errorf("unsupported requirements format: %s", ext)
		}

		var storedReqs []*graph.StoredRequirement
		for _, req := range parsed.Requirements {
			storedReqs = append(storedReqs, &graph.StoredRequirement{
				ExternalID:         req.ExternalID,
				Title:              req.Title,
				Description:        req.Description,
				Source:             filepath.Base(traceReqFile),
				Priority:           req.Priority,
				AcceptanceCriteria: req.AcceptanceCriteria,
				Tags:               req.Tags,
			})
		}
		store.StoreRequirements(repo.ID, storedReqs)
	}

	// Find the requirement by external ID
	req := store.GetRequirementByExternalID(repo.ID, reqID)
	if req == nil {
		fmt.Fprintf(os.Stderr, "Requirement %q not found.\n", reqID)
		fmt.Fprintf(os.Stderr, "Available requirements:\n")
		reqs, _ := store.GetRequirements(repo.ID, 100, 0)
		for _, r := range reqs {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", r.ExternalID, r.Title)
		}
		return fmt.Errorf("requirement not found: %s", reqID)
	}

	// Find links by scanning symbols for comment references
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 10000, 0)
	for _, sym := range syms {
		// Check doc comments for requirement references
		if containsReqRef(sym.DocComment, reqID) || containsReqRef(sym.Signature, reqID) {
			store.StoreLink(repo.ID, &graph.StoredLink{
				RequirementID: req.ID,
				SymbolID:      sym.ID,
				Confidence:    0.95,
				Source:        "comment",
				LinkType:      "implements",
				Rationale:     fmt.Sprintf("Comment reference to %s in %s:%s", reqID, sym.FilePath, sym.Name),
			})
		}
	}

	// Retrieve links
	links := store.GetLinksForRequirement(req.ID, false)

	// Filter by confidence
	var filtered []*graph.StoredLink
	for _, l := range links {
		if l.Confidence >= traceMinConf {
			filtered = append(filtered, l)
		}
	}

	// Sort by confidence descending
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Confidence > filtered[j].Confidence
	})

	// Build output
	tr := traceResult{
		RequirementID: req.ExternalID,
		Title:         req.Title,
	}

	for _, l := range filtered {
		// Look up symbol info
		symName := l.SymbolID
		filePath := ""
		startLine := 0
		endLine := 0
		for _, sym := range syms {
			if sym.ID == l.SymbolID {
				symName = sym.Name
				filePath = sym.FilePath
				startLine = sym.StartLine
				endLine = sym.EndLine
				break
			}
		}

		tr.Links = append(tr.Links, traceLink{
			SymbolName: symName,
			FilePath:   filePath,
			StartLine:  startLine,
			EndLine:    endLine,
			Confidence: l.Confidence,
			Source:     l.Source,
			Rationale:  l.Rationale,
		})
	}

	if traceJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tr)
	}

	// Pretty print
	fmt.Fprintf(os.Stdout, "\nRequirement: %s\n", tr.RequirementID)
	fmt.Fprintf(os.Stdout, "Title:       %s\n", tr.Title)
	fmt.Fprintf(os.Stdout, "Links:       %d\n\n", len(tr.Links))

	if len(tr.Links) == 0 {
		fmt.Fprintln(os.Stdout, "  No linked code found.")
		return nil
	}

	for _, link := range tr.Links {
		conf := fmt.Sprintf("%.0f%%", link.Confidence*100)
		fmt.Fprintf(os.Stdout, "  %s  %s:%s (lines %d-%d)\n",
			conf, link.FilePath, link.SymbolName, link.StartLine, link.EndLine)
		fmt.Fprintf(os.Stdout, "         source: %s\n", link.Source)
		if link.Rationale != "" {
			fmt.Fprintf(os.Stdout, "         reason: %s\n", link.Rationale)
		}
		fmt.Fprintln(os.Stdout)
	}

	return nil
}

func containsReqRef(text, reqID string) bool {
	if text == "" {
		return false
	}
	return strings.Contains(text, reqID)
}
