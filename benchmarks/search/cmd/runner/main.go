// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Benchmark runner for the hybrid retrieval service.
//
// Loads the frozen query set from queries.yaml, hits the running
// SourceBridge GraphQL API for each query, and writes JSONL records
// (query → returned top-N) to a timestamped file under
// benchmarks/search/reports/<name>/. The companion report.py (or any
// ad-hoc analysis script) computes MRR@10, NDCG@10, Recall@10, and
// latency percentiles against labels.yaml.
//
// Usage:
//
//	go run ./benchmarks/search/cmd/runner \
//	  -url http://localhost:8080/api/v1/graphql \
//	  -token <jwt> \
//	  -out benchmarks/search/reports/2026-04-22_baseline/
//
// The runner does not compute scores itself — that is the report
// script's job. This keeps the runner dumb and reusable for both
// baseline and candidate runs.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type queryFile struct {
	Queries []queryEntry `yaml:"queries"`
}

type queryEntry struct {
	ID                    string `yaml:"id"`
	RepoID                string `yaml:"repo_id"`
	RepoName              string `yaml:"repo_name"`
	Query                 string `yaml:"query"`
	QueryClass            string `yaml:"query_class"`
	RequirementLinkCohort string `yaml:"requirement_link_cohort"`
	Notes                 string `yaml:"notes"`
}

type graphqlReq struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type graphqlResp struct {
	Data struct {
		Search []struct {
			Type           string   `json:"type"`
			ID             string   `json:"id"`
			Title          string   `json:"title"`
			Description    *string  `json:"description"`
			FilePath       *string  `json:"filePath"`
			Line           *int     `json:"line"`
			RepositoryID   string   `json:"repositoryId"`
			RepositoryName string   `json:"repositoryName"`
			Score          *float64 `json:"score"`
			Signals        *struct {
				Exact       *float64 `json:"exact"`
				Lexical     *float64 `json:"lexical"`
				Semantic    *float64 `json:"semantic"`
				Graph       *float64 `json:"graph"`
				Requirement *float64 `json:"requirement"`
			} `json:"signals"`
		} `json:"search"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

// jsonlRecord is what we write per query. Shape is deliberately small
// and explicit so report.py (or any ad-hoc pandas/duckdb analysis) can
// consume it directly.
type jsonlRecord struct {
	QueryID               string                 `json:"query_id"`
	Query                 string                 `json:"query"`
	RepoID                string                 `json:"repo_id"`
	QueryClass            string                 `json:"query_class"`
	RequirementLinkCohort string                 `json:"requirement_link_cohort"`
	LatencyMs             float64                `json:"latency_ms"`
	Results               []map[string]any       `json:"results"`
	Err                   string                 `json:"error,omitempty"`
	Env                   map[string]interface{} `json:"env,omitempty"`
}

const searchGraphQL = `query Search($query: String!, $repositoryId: ID, $limit: Int) {
  search(query: $query, repositoryId: $repositoryId, limit: $limit) {
    type id title description filePath line repositoryId repositoryName score
    signals { exact lexical semantic graph requirement }
  }
}`

func main() {
	var (
		url         = flag.String("url", "http://localhost:8080/api/v1/graphql", "GraphQL endpoint")
		token       = flag.String("token", "", "Bearer token (JWT or API token) forwarded as Authorization")
		queriesPath = flag.String("queries", "benchmarks/search/queries.yaml", "path to queries.yaml")
		outDir      = flag.String("out", "", "output directory (defaults to benchmarks/search/reports/<timestamp>_run)")
		limit       = flag.Int("limit", 10, "search limit passed into GraphQL")
		runName     = flag.String("name", "", "optional run name suffix used when -out is not given")
		label       = flag.String("label", "candidate", "run label tagged onto every record (e.g. baseline / candidate)")
	)
	flag.Parse()

	qs, err := loadQueries(*queriesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load queries:", err)
		os.Exit(1)
	}
	if len(qs) == 0 {
		fmt.Fprintln(os.Stderr, "no queries loaded from", *queriesPath)
		os.Exit(1)
	}

	if *outDir == "" {
		stamp := time.Now().Format("2006-01-02_150405")
		name := *runName
		if name == "" {
			name = "run"
		}
		*outDir = filepath.Join("benchmarks/search/reports", stamp+"_"+name)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	outPath := filepath.Join(*outDir, *label+".jsonl")
	out, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create out:", err)
		os.Exit(1)
	}
	defer out.Close()

	envPath := filepath.Join(*outDir, "environment.yaml")
	_ = writeEnvironment(envPath, *url, *label, *limit, len(qs))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	fmt.Fprintf(os.Stderr, "running %d queries against %s (label=%s)\n", len(qs), *url, *label)

	ok := 0
	failed := 0
	start := time.Now()
	for _, q := range qs {
		rec := runOne(httpClient, *url, *token, q, *limit)
		b, _ := json.Marshal(rec)
		if _, err := out.Write(append(b, '\n')); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		if rec.Err != "" {
			failed++
		} else {
			ok++
		}
	}
	fmt.Fprintf(os.Stderr, "done: ok=%d failed=%d total_wall=%s\nwrote %s\n",
		ok, failed, time.Since(start).Round(time.Millisecond), outPath)
}

func loadQueries(path string) ([]queryEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qf queryFile
	if err := yaml.Unmarshal(raw, &qf); err != nil {
		return nil, err
	}
	return qf.Queries, nil
}

func runOne(c *http.Client, url, token string, q queryEntry, limit int) jsonlRecord {
	rec := jsonlRecord{
		QueryID:               q.ID,
		Query:                 q.Query,
		RepoID:                q.RepoID,
		QueryClass:            q.QueryClass,
		RequirementLinkCohort: q.RequirementLinkCohort,
	}
	vars := map[string]interface{}{
		"query": q.Query,
		"limit": limit,
	}
	if q.RepoID != "" {
		vars["repositoryId"] = q.RepoID
	}
	body, _ := json.Marshal(graphqlReq{Query: searchGraphQL, Variables: vars})

	start := time.Now()
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		rec.Err = err.Error()
		return rec
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		rec.Err = err.Error()
		rec.LatencyMs = msSince(start)
		return rec
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	rec.LatencyMs = msSince(start)
	if err != nil {
		rec.Err = err.Error()
		return rec
	}
	if resp.StatusCode >= 400 {
		rec.Err = fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(raw), 300))
		return rec
	}
	var gql graphqlResp
	if err := json.Unmarshal(raw, &gql); err != nil {
		rec.Err = err.Error()
		return rec
	}
	if len(gql.Errors) > 0 {
		msgs := make([]string, len(gql.Errors))
		for i, e := range gql.Errors {
			msgs[i] = e.Message
		}
		rec.Err = errors.Join(errFromStrs(msgs)...).Error()
		return rec
	}
	rec.Results = make([]map[string]any, 0, len(gql.Data.Search))
	for _, r := range gql.Data.Search {
		m := map[string]any{
			"entity_type":     r.Type,
			"entity_id":       r.ID,
			"title":           r.Title,
			"repo_id":         r.RepositoryID,
			"repo_name":       r.RepositoryName,
			"file_path":       r.FilePath,
			"line":            r.Line,
			"score":           r.Score,
		}
		if r.Signals != nil {
			m["signals"] = map[string]any{
				"exact":       r.Signals.Exact,
				"lexical":     r.Signals.Lexical,
				"semantic":    r.Signals.Semantic,
				"graph":       r.Signals.Graph,
				"requirement": r.Signals.Requirement,
			}
		}
		rec.Results = append(rec.Results, m)
	}
	return rec
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Nanoseconds()) / 1e6
}

func errFromStrs(s []string) []error {
	out := make([]error, len(s))
	for i, x := range s {
		out[i] = errors.New(x)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func writeEnvironment(path, url, label string, limit, nQueries int) error {
	env := map[string]interface{}{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"url":          url,
		"label":        label,
		"limit":        limit,
		"n_queries":    nQueries,
	}
	b, err := yaml.Marshal(env)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
