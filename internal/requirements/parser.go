// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package requirements

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Requirement represents a parsed requirement.
type Requirement struct {
	ExternalID         string
	Title              string
	Description        string
	Priority           string
	AcceptanceCriteria []string
	Tags               []string
}

// ParseResult contains parsing output.
type ParseResult struct {
	Requirements []Requirement
	Warnings     []string
}

var (
	headingRE  = regexp.MustCompile(`(?m)^##\s+([A-Z]+-\d+):\s*(.+)$`)
	priorityRE = regexp.MustCompile(`\*\*Priority:\*\*\s*(\w+)`)
)

// ParseMarkdown parses a markdown document and extracts requirements.
func ParseMarkdown(content string) *ParseResult {
	result := &ParseResult{}

	sections := splitSections(content)
	for _, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}

		match := headingRE.FindStringSubmatch(section)
		if match == nil {
			continue
		}

		reqID := match[1]
		title := strings.TrimSpace(match[2])

		lines := strings.Split(section, "\n")
		var descLines []string
		var criteria []string
		priority := ""
		inCriteria := false

		for _, line := range lines[1:] {
			stripped := strings.TrimSpace(line)

			if stripped == "" {
				continue
			}

			if m := priorityRE.FindStringSubmatch(stripped); m != nil {
				priority = m[1]
				continue
			}

			if strings.Contains(stripped, "Acceptance Criteria") {
				inCriteria = true
				continue
			}

			if inCriteria && strings.HasPrefix(stripped, "- ") {
				criteria = append(criteria, strings.TrimSpace(stripped[2:]))
				continue
			}

			if !inCriteria && !strings.HasPrefix(stripped, "- **") {
				descLines = append(descLines, stripped)
			}
		}

		result.Requirements = append(result.Requirements, Requirement{
			ExternalID:         reqID,
			Title:              title,
			Description:        strings.Join(descLines, " "),
			Priority:           priority,
			AcceptanceCriteria: criteria,
		})
	}

	return result
}

func splitSections(content string) []string {
	var sections []string
	current := ""
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") && current != "" {
			sections = append(sections, current)
			current = ""
		}
		current += line + "\n"
	}
	if current != "" {
		sections = append(sections, current)
	}
	return sections
}

// DefaultCSVColumns maps logical field names to default CSV column names.
var DefaultCSVColumns = map[string]string{
	"id":                  "id",
	"title":               "title",
	"description":         "description",
	"priority":            "priority",
	"acceptance_criteria": "acceptance_criteria",
}

// ParseCSV parses a CSV string and extracts requirements.
func ParseCSV(content string, columnMapping map[string]string) *ParseResult {
	result := &ParseResult{}

	if columnMapping == nil {
		columnMapping = DefaultCSVColumns
	}

	reader := csv.NewReader(strings.NewReader(content))

	// Read header
	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return result
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("error reading CSV header: %v", err))
		return result
	}

	// Map column names to indices
	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("error reading CSV row: %v", err))
			continue
		}

		getCol := func(logical string) string {
			col := columnMapping[logical]
			if idx, ok := colIdx[col]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}

		reqID := getCol("id")
		title := getCol("title")
		if reqID == "" || title == "" {
			continue
		}

		var criteria []string
		raw := getCol("acceptance_criteria")
		if raw != "" {
			for _, c := range strings.Split(raw, ";") {
				c = strings.TrimSpace(c)
				if c != "" {
					criteria = append(criteria, c)
				}
			}
		}

		result.Requirements = append(result.Requirements, Requirement{
			ExternalID:         reqID,
			Title:              title,
			Description:        getCol("description"),
			Priority:           getCol("priority"),
			AcceptanceCriteria: criteria,
		})
	}

	return result
}
