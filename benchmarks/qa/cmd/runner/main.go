// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Command runner drives a single arm of the QA parity benchmark.
//
// Usage:
//
//	runner -arm=baseline  -questions=questions.yaml -out=reports/2026-04-22_baseline
//	runner -arm=candidate -questions=questions.yaml -out=reports/2026-04-22_candidate \
//	       -server-url=https://sourcebridge.example.com -api-token=$SOURCEBRIDGE_API_TOKEN
//
// One run == one arm. Pair two runs with report.py to get the
// signed-off paired report.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type question struct {
	ID       string `yaml:"id"`
	Repo     string `yaml:"repo"`
	Class    string `yaml:"class"`
	Question string `yaml:"question"`
	Notes    string `yaml:"notes"`
}

type questionsFile struct {
	Version   int        `yaml:"version"`
	Questions []question `yaml:"questions"`
}

type sample struct {
	ID           string         `json:"id"`
	Repo         string         `json:"repo"`
	Class        string         `json:"class"`
	Question     string         `json:"question"`
	Arm          string         `json:"arm"`
	Answer       string         `json:"answer"`
	References   []string       `json:"references"`
	Diagnostics  map[string]any `json:"diagnostics"`
	Usage        map[string]int `json:"usage"`
	ElapsedMs    int64          `json:"elapsed_ms"`
	ErrorKind    string         `json:"error_kind,omitempty"`
	FallbackUsed string         `json:"fallback_used,omitempty"`
}

type environment struct {
	Arm           string    `yaml:"arm"`
	Date          time.Time `yaml:"date"`
	CommitSHA     string    `yaml:"commit_sha"`
	ServerURL     string    `yaml:"server_url,omitempty"`
	Mode          string    `yaml:"mode"`
	QuestionsHash string    `yaml:"questions_hash"`
	Notes         string    `yaml:"notes,omitempty"`
}

func main() {
	var (
		arm          string
		questionsP   string
		out          string
		serverURL    string
		apiToken     string
		mode         string
		repositoryID string
		workersDir   string
		notes        string
	)
	flag.StringVar(&arm, "arm", "", "Benchmark arm label: baseline | candidate (required)")
	flag.StringVar(&questionsP, "questions", "questions.yaml", "Path to questions.yaml")
	flag.StringVar(&out, "out", "", "Output directory for run.jsonl + environment.yaml (required)")
	flag.StringVar(&serverURL, "server-url", "", "Server URL for candidate arm")
	flag.StringVar(&apiToken, "api-token", os.Getenv("SOURCEBRIDGE_API_TOKEN"), "API token for candidate arm")
	flag.StringVar(&mode, "mode", "deep", "QA mode: fast | deep")
	flag.StringVar(&repositoryID, "repository-id", "", "Repository ID (required for candidate arm; ignored for baseline)")
	flag.StringVar(&workersDir, "workers-dir", "./workers", "Path to the workers/ dir (baseline arm only)")
	flag.StringVar(&notes, "notes", "", "Free-form notes recorded in environment.yaml")
	flag.Parse()

	if arm == "" || out == "" {
		flag.Usage()
		die(errors.New("-arm and -out are required"))
	}
	if arm != "baseline" && arm != "candidate" {
		die(fmt.Errorf("-arm must be baseline or candidate, got %q", arm))
	}

	qs, err := loadQuestions(questionsP)
	if err != nil {
		die(err)
	}
	if len(qs.Questions) == 0 {
		fmt.Fprintln(os.Stderr, "WARNING: questions.yaml is empty; writing a smoke run with no samples")
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		die(err)
	}
	runPath := filepath.Join(out, "run.jsonl")
	f, err := os.Create(runPath)
	if err != nil {
		die(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	ctx := context.Background()
	for _, q := range qs.Questions {
		s := sample{
			ID:       q.ID,
			Repo:     q.Repo,
			Class:    q.Class,
			Question: q.Question,
			Arm:      arm,
		}
		start := time.Now()
		switch arm {
		case "baseline":
			resp, err := runBaseline(ctx, workersDir, q.Question, mode)
			s.ElapsedMs = time.Since(start).Milliseconds()
			if err != nil {
				s.ErrorKind = "baseline_error"
				s.Answer = err.Error()
			} else {
				s.fromBaselineResp(resp)
			}
		case "candidate":
			if serverURL == "" || repositoryID == "" {
				die(errors.New("-server-url and -repository-id are required for candidate arm"))
			}
			resp, err := runCandidate(ctx, serverURL, apiToken, repositoryID, q.Question, mode)
			s.ElapsedMs = time.Since(start).Milliseconds()
			if err != nil {
				s.ErrorKind = "candidate_error"
				s.Answer = err.Error()
			} else {
				s.fromCandidateResp(resp)
			}
		}
		if err := enc.Encode(&s); err != nil {
			die(err)
		}
	}

	envPath := filepath.Join(out, "environment.yaml")
	envF, err := os.Create(envPath)
	if err != nil {
		die(err)
	}
	defer envF.Close()
	env := environment{
		Arm:       arm,
		Date:      time.Now().UTC(),
		CommitSHA: runGit(ctx, "rev-parse", "--short", "HEAD"),
		ServerURL: serverURL,
		Mode:      mode,
		Notes:     notes,
	}
	if err := yaml.NewEncoder(envF).Encode(env); err != nil {
		die(err)
	}

	fmt.Printf("wrote %d samples to %s\n", len(qs.Questions), runPath)
}

func loadQuestions(path string) (*questionsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read questions: %w", err)
	}
	var out questionsFile
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse questions: %w", err)
	}
	return &out, nil
}

func runBaseline(ctx context.Context, workersDir, question, mode string) (map[string]any, error) {
	absWorkers, err := filepath.Abs(workersDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "uv", "run", "python", "cli_ask.py", question, mode)
	cmd.Dir = absWorkers
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("python baseline failed: %w (stderr: %s)", err, stderr.String())
	}
	var m map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		return nil, fmt.Errorf("baseline response parse: %w", err)
	}
	return m, nil
}

func runCandidate(ctx context.Context, serverURL, token, repoID, question, mode string) (map[string]any, error) {
	payload, _ := json.Marshal(map[string]any{
		"repositoryId": repoID,
		"question":     question,
		"mode":         mode,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/api/v1/ask", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("candidate %d: %s", resp.StatusCode, string(body))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("candidate response parse: %w", err)
	}
	return m, nil
}

func (s *sample) fromBaselineResp(m map[string]any) {
	if ans, ok := m["answer"].(string); ok {
		s.Answer = ans
	}
	if refs, ok := m["references"].([]any); ok {
		for _, r := range refs {
			if rs, ok := r.(string); ok {
				s.References = append(s.References, rs)
			}
		}
	}
	if d, ok := m["diagnostics"].(map[string]any); ok {
		s.Diagnostics = d
		if fb, ok := d["fallback_used"].(string); ok {
			s.FallbackUsed = fb
		}
	}
	if u, ok := m["usage"].(map[string]any); ok {
		s.Usage = make(map[string]int)
		if n, ok := u["input_tokens"].(float64); ok {
			s.Usage["input_tokens"] = int(n)
		}
		if n, ok := u["output_tokens"].(float64); ok {
			s.Usage["output_tokens"] = int(n)
		}
	}
}

func (s *sample) fromCandidateResp(m map[string]any) {
	if ans, ok := m["answer"].(string); ok {
		s.Answer = ans
	}
	if refs, ok := m["references"].([]any); ok {
		for _, r := range refs {
			rm, _ := r.(map[string]any)
			if t, ok := rm["title"].(string); ok {
				s.References = append(s.References, t)
			}
		}
	}
	if d, ok := m["diagnostics"].(map[string]any); ok {
		s.Diagnostics = d
		if fb, ok := d["fallbackUsed"].(string); ok {
			s.FallbackUsed = fb
		}
	}
	if u, ok := m["usage"].(map[string]any); ok {
		s.Usage = make(map[string]int)
		if n, ok := u["inputTokens"].(float64); ok {
			s.Usage["input_tokens"] = int(n)
		}
		if n, ok := u["outputTokens"].(float64); ok {
			s.Usage["output_tokens"] = int(n)
		}
	}
}

func runGit(ctx context.Context, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "runner:", err)
	os.Exit(1)
}
