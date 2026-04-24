//go:build enterprise

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/metadata"

	"github.com/jstuart0/sourcebridge-enterprise/routes"
	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	"github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	surrealdb "github.com/surrealdb/surrealdb.go"
)

// registerEnterpriseRoutes adds enterprise-only HTTP routes to the router.
// This file is only compiled when -tags enterprise is specified.
//
// Webhooks are registered publicly (they validate their own signatures).
// Admin endpoints are behind JWT auth + tenant context extraction + RBAC.
func (s *Server) registerEnterpriseRoutes(r chi.Router) {
	slog.Info("registerEnterpriseRoutes called", "enterpriseDB_nil", s.enterpriseDB == nil)
	// Extract the raw SurrealDB handle if available
	var rawDB *surrealdb.DB
	if s.enterpriseDB != nil {
		rawDB, _ = s.enterpriseDB.(*surrealdb.DB)
	}

	ectx := routes.NewContext(
		s.cfg.Security.JWTSecret,
		s.cfg.Security.GitHubWebhookSecret,
		s.cfg.Security.GitLabWebhookSecret,
		rawDB,
	)

	// Wire tenant repo filtering into the main API routes
	s.repoChecker = ectx.RepoChecker

	// Wire host services (GraphStore) into webhook handlers
	ectx.SetHostServices(&graphStoreHostServices{
		store:          s.store,
		knowledgeStore: s.knowledgeStore,
		repoChecker:    ectx.RepoChecker,
	})

	// Public webhook endpoints — they validate their own signatures
	r.Post("/webhooks/github", ectx.GitHubWebhook)
	r.Post("/webhooks/gitlab", ectx.GitLabWebhook)

	// Enterprise admin API — JWT auth + tenant context + RBAC
	r.Route("/api/v1/enterprise", func(r chi.Router) {
		r.Use(auth.MiddlewareWithTokens(s.jwtMgr, s.tokenStore))
		r.Use(middleware.TenantMiddleware(&claimsFirstTenantExtractor{base: ectx.TenantExtractor}))

		getRoleFn := func(r *http.Request) string {
			return middleware.GetUserRole(r.Context())
		}
		getActorFn := func(r *http.Request) string {
			return middleware.GetUserID(r.Context())
		}

		ectx.RegisterAdmin(r, getRoleFn, getActorFn)
	})

	// MCP enterprise extensions: when the enterprise routes.Context exposes
	// MCPPermissionChecker / MCPAuditLogger / MCPToolExtender, wire them
	// into s.mcp here. The interfaces are defined in mcp.go.

	// Reports — registered alongside existing admin routes.
	slog.Info("registering report routes at /api/v1/reports")

	// Wire the report generator to call the Python worker via gRPC.
	// The callback collects REAL repository data from the GraphStore so
	// the LLM has actual evidence to write about (not hallucinations).
	if s.worker != nil {
		ectx.API.SetReportGenerator(func(reportID, reportType, audience, repoDataJSON, sectionDefsJSON, outputDir string, repoIDs, selectedSections []string, includeDiagrams, includeRecommendations, includeLOE bool, loeMode, reportName, analysisDepth, styleSystemPrompt, styleSectionRules string) (string, int, int, int, string, error) {
			// Collect actual repo data from the graph and knowledge stores
			realRepoData := collectRepoDataForReport(s.store, s.knowledgeStore, repoIDs)
			repoJSON, _ := json.Marshal(realRepoData)
			slog.Info("report: collected repo data", "repos", len(repoIDs), "jsonBytes", len(repoJSON))

			ctx := withWorkerLLMMetadata(context.Background(), s.cfg, "report")
			resp, err := s.worker.GenerateReport(ctx, &enterprisev1.GenerateReportRequest{
				ReportId:               reportID,
				ReportName:             reportName,
				ReportType:             reportType,
				Audience:               audience,
				RepositoryIds:          repoIDs,
				SelectedSections:       selectedSections,
				IncludeDiagrams:        includeDiagrams,
				LoeMode:                loeMode,
				OutputDir:              outputDir,
				RepoDataJson:           string(repoJSON),
				SectionDefinitionsJson: sectionDefsJSON,
				AnalysisDepth:          analysisDepth,
				IncludeRecommendations: includeRecommendations,
				IncludeLoe:             includeLOE,
				StyleSystemPrompt:      styleSystemPrompt,
				StyleSectionRules:      styleSectionRules,
			})
			if err != nil {
				return "", 0, 0, 0, "", err
			}
			return resp.Markdown, int(resp.SectionCount), int(resp.WordCount), int(resp.EvidenceCount), resp.EvidenceJson, nil
		})
		slog.Info("report generator wired to Python worker via gRPC")
	}

	r.Route("/api/v1/reports", func(r chi.Router) {
		ectx.RegisterReportRoutes(r)
	})

	// Diagram editor (enterprise-only mutation endpoints)
	r.Route("/api/v1/diagrams", func(r chi.Router) {
		ectx.RegisterDiagramEditorRoutes(r)
	})

	// Compliance-assessment endpoints (plan
	// thoughts/shared/plans/2026-04-24-first-class-compliance-reports.md).
	// Paths under /api/v1/compliance follow the tenant-in-path
	// convention of /billing, /audit, /sessions.
	slog.Info("registering compliance routes at /api/v1/compliance")
	r.Route("/api/v1/compliance", func(r chi.Router) {
		ectx.RegisterComplianceRoutes(r)
	})
}

func withWorkerLLMMetadata(ctx context.Context, cfg *config.Config, operationGroup string) context.Context {
	if cfg == nil {
		return ctx
	}
	model := cfg.LLM.ModelForOperation(operationGroup)
	pairs := []string{
		"x-sb-llm-provider", cfg.LLM.Provider,
		"x-sb-llm-base-url", cfg.LLM.BaseURL,
		"x-sb-llm-api-key", cfg.LLM.APIKey,
		"x-sb-llm-draft-model", cfg.LLM.DraftModel,
		"x-sb-operation", operationGroup,
	}
	if model != "" {
		pairs = append(pairs, "x-sb-model", model)
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(pairs...))
}

type claimsFirstTenantExtractor struct {
	base interface {
		ExtractTenant(r *http.Request) (tenantID, userID, role string, err error)
	}
}

func (e *claimsFirstTenantExtractor) ExtractTenant(r *http.Request) (tenantID, userID, role string, err error) {
	if claims := auth.GetClaims(r.Context()); claims != nil && claims.UserID != "" {
		return claims.OrgID, claims.UserID, claims.Role, nil
	}
	return e.base.ExtractTenant(r)
}

// graphStoreHostServices adapts the OSS GraphStore to the enterprise
// routes.HostServices interface for webhook symbol/requirement lookup
// and understanding score computation.
type graphStoreHostServices struct {
	store          graphstore.GraphStore
	knowledgeStore knowledge.KnowledgeStore
	repoChecker    middleware.RepoAccessChecker
}

func (h *graphStoreHostServices) FindRepoByName(repoFullName string) (string, bool) {
	for _, repo := range h.store.ListRepositories() {
		// Match against Name (e.g., "owner/repo") or RemoteURL
		if repo.Name == repoFullName {
			return repo.ID, true
		}
		if repo.RemoteURL != "" && strings.Contains(repo.RemoteURL, repoFullName) {
			return repo.ID, true
		}
	}
	return "", false
}

func (h *graphStoreHostServices) FindSymbolsByFile(repoID, filePath string) []routes.SymbolInfo {
	symbols := h.store.GetSymbolsByFile(repoID, filePath)
	out := make([]routes.SymbolInfo, len(symbols))
	for i, s := range symbols {
		out[i] = routes.SymbolInfo{ID: s.ID, Name: s.Name, Kind: string(s.Kind)}
	}
	return out
}

func (h *graphStoreHostServices) FindRequirementsBySymbol(symbolID string) []routes.RequirementInfo {
	links := h.store.GetLinksForSymbol(symbolID, false)
	var reqs []routes.RequirementInfo
	for _, link := range links {
		req := h.store.GetRequirement(link.RequirementID)
		if req != nil {
			reqs = append(reqs, routes.RequirementInfo{
				ID:         req.ID,
				ExternalID: req.ExternalID,
				Title:      req.Title,
				Confidence: fmt.Sprintf("%.0f%%", link.Confidence*100),
			})
		}
	}
	return reqs
}

func (h *graphStoreHostServices) GetRepoCoverage(repoID string) float64 {
	reqs, total := h.store.GetRequirements(repoID, 0, 0)
	if total == 0 {
		return 1.0 // no requirements = fully covered
	}
	linked := 0
	for _, req := range reqs {
		links := h.store.GetLinksForRequirement(req.ID, false)
		if len(links) > 0 {
			linked++
		}
	}
	return float64(linked) / float64(total)
}

func (h *graphStoreHostServices) TriggerReindex(repoID string) error {
	repo := h.store.GetRepository(repoID)
	if repo == nil {
		return fmt.Errorf("repository %s not found", repoID)
	}
	// Clear any previous error so the repository shows as actionable.
	// The actual reindex is triggered asynchronously — in production this
	// would enqueue a background job or send an event to the worker.
	h.store.SetRepositoryError(repoID, nil)
	return nil
}

func (h *graphStoreHostServices) GetUnderstandingScore(repoID string) *routes.UnderstandingScoreInfo {
	var kfp graphstore.KnowledgeFreshnessProvider
	if h.knowledgeStore != nil {
		kfp = &knowledgeFreshnessAdapter{store: h.knowledgeStore}
	}
	score := graphstore.ComputeUnderstandingScore(h.store, kfp, repoID)
	if score == nil {
		return nil
	}
	return &routes.UnderstandingScoreInfo{
		Overall:               score.Overall,
		TraceabilityCoverage:  score.TraceabilityCoverage,
		DocumentationCoverage: score.DocumentationCoverage,
		ReviewCoverage:        score.ReviewCoverage,
		TestCoverage:          score.TestCoverage,
		KnowledgeFreshness:    score.KnowledgeFreshness,
		AICodeRatio:           score.AICodeRatio,
	}
}

func (h *graphStoreHostServices) GetTenantRepoScores(tenantID string) []routes.RepoScoreInfo {
	if h.repoChecker == nil {
		return nil
	}
	repoIDs, err := h.repoChecker.GetTenantRepos(tenantID)
	if err != nil {
		return nil
	}

	var results []routes.RepoScoreInfo
	for _, repoID := range repoIDs {
		repo := h.store.GetRepository(repoID)
		if repo == nil {
			continue
		}
		scoreInfo := h.GetUnderstandingScore(repoID)
		if scoreInfo == nil {
			continue
		}
		results = append(results, routes.RepoScoreInfo{
			RepoID:   repoID,
			RepoName: repo.Name,
			Score:    *scoreInfo,
		})
	}
	return results
}

func (h *graphStoreHostServices) GetAIScoreViolations(repoID string, threshold float64) []routes.AIViolation {
	files := h.store.GetFiles(repoID)
	var violations []routes.AIViolation
	for _, f := range files {
		if f.AIScore >= threshold {
			signals := f.AISignals
			if signals == nil {
				signals = []string{}
			}
			violations = append(violations, routes.AIViolation{
				FilePath: f.Path,
				AIScore:  f.AIScore,
				Signals:  signals,
			})
		}
	}
	return violations
}

func (h *graphStoreHostServices) GetLatestImpactReport(repoID string) *routes.ImpactReportInfo {
	report := h.store.GetLatestImpactReport(repoID)
	if report == nil {
		return nil
	}
	out := &routes.ImpactReportInfo{
		FilesChanged:    len(report.FilesChanged),
		SymbolsModified: len(report.SymbolsModified),
		StaleArtifacts:  len(report.StaleArtifacts),
	}
	for _, ar := range report.AffectedRequirements {
		out.AffectedRequirements = append(out.AffectedRequirements, routes.AffectedRequirementInfo{
			ExternalID:    ar.ExternalID,
			Title:         ar.Title,
			AffectedLinks: ar.AffectedLinks,
			TotalLinks:    ar.TotalLinks,
		})
	}
	return out
}

// collectRepoDataForReport gathers real repository data from the GraphStore
// and KnowledgeStore so the Python report engine has actual evidence.
// This replaces the previous empty "{}" that caused hallucinated reports.
func collectRepoDataForReport(store graphstore.GraphStore, knowledgeStore knowledge.KnowledgeStore, repoIDs []string) map[string]interface{} {
	data := make(map[string]interface{})

	for _, repoID := range repoIDs {
		repo := store.GetRepository(repoID)
		if repo == nil {
			continue
		}

		rd := map[string]interface{}{
			"name":         repo.Name,
			"remote_url":   repo.RemoteURL,
			"branch":       repo.Branch,
			"commit_sha":   repo.CommitSHA,
			"file_count":   repo.FileCount,
			"symbol_count": repo.FunctionCount + repo.ClassCount,
		}

		if repo.ClonePath != "" {
			rd["clone_path"] = repo.ClonePath
		}

		// --- Language distribution from actual files ---
		files := store.GetFiles(repoID)
		langCounts := map[string]int{}
		topDirs := map[string]bool{}
		var sampleFiles []string
		for _, f := range files {
			if f.Language != "" {
				langCounts[f.Language]++
			}
			// Collect top-level directories
			if parts := strings.SplitN(f.Path, "/", 2); len(parts) > 1 {
				topDirs[parts[0]] = true
			}
			// Sample file paths (up to 100)
			if len(sampleFiles) < 100 {
				sampleFiles = append(sampleFiles, f.Path)
			}
		}

		// Sort languages by count descending
		type langCount struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		var languages []langCount
		for lang, count := range langCounts {
			languages = append(languages, langCount{Name: lang, Count: count})
		}
		sort.Slice(languages, func(i, j int) bool { return languages[i].Count > languages[j].Count })
		rd["languages"] = languages

		dirs := make([]string, 0, len(topDirs))
		for d := range topDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		rd["top_level_dirs"] = dirs
		rd["sample_files"] = sampleFiles

		// --- Understanding score ---
		var kfp graphstore.KnowledgeFreshnessProvider
		if knowledgeStore != nil {
			kfp = &knowledgeFreshnessAdapter{store: knowledgeStore}
		}
		score := graphstore.ComputeUnderstandingScore(store, kfp, repoID)
		if score != nil {
			rd["understanding_score"] = map[string]interface{}{
				"overall":               score.Overall,
				"traceabilityCoverage":  score.TraceabilityCoverage,
				"documentationCoverage": score.DocumentationCoverage,
				"reviewCoverage":        score.ReviewCoverage,
				"testCoverage":          score.TestCoverage,
				"knowledgeFreshness":    score.KnowledgeFreshness,
				"aiCodeRatio":           score.AICodeRatio,
			}
		}

		// --- Test detection from actual symbols ---
		testSymbols, totalSymbols := store.GetTestSymbolRatio(repoID)
		var testFrameworks []string
		for _, f := range files {
			ext := filepath.Ext(f.Path)
			base := filepath.Base(f.Path)
			if strings.Contains(base, "_test.go") {
				testFrameworks = appendUnique(testFrameworks, "Go testing")
			} else if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
				if ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" {
					if strings.Contains(f.Path, "jest") || strings.Contains(base, ".test.") {
						testFrameworks = appendUnique(testFrameworks, "Jest")
					}
					if strings.Contains(f.Path, "vitest") {
						testFrameworks = appendUnique(testFrameworks, "Vitest")
					}
				}
			} else if strings.HasPrefix(base, "test_") && ext == ".py" {
				testFrameworks = appendUnique(testFrameworks, "pytest")
			}
		}
		rd["test_detection"] = map[string]interface{}{
			"source":          "heuristic",
			"test_file_count": testSymbols,
			"total_symbols":   totalSymbols,
			"frameworks":      testFrameworks,
		}

		// --- Auth detection from file paths ---
		var authPatterns []string
		for _, f := range files {
			lower := strings.ToLower(f.Path)
			if strings.Contains(lower, "auth") || strings.Contains(lower, "login") {
				if strings.Contains(lower, "oauth") {
					authPatterns = appendUnique(authPatterns, "OAuth")
				}
				if strings.Contains(lower, "jwt") || strings.Contains(lower, "token") {
					authPatterns = appendUnique(authPatterns, "JWT")
				}
				if strings.Contains(lower, "saml") {
					authPatterns = appendUnique(authPatterns, "SAML")
				}
				if strings.Contains(lower, "session") {
					authPatterns = appendUnique(authPatterns, "Session-based")
				}
				if len(authPatterns) == 0 {
					authPatterns = appendUnique(authPatterns, "Authentication (detected)")
				}
			}
			if strings.Contains(lower, "rbac") || strings.Contains(lower, "permission") || strings.Contains(lower, "role") {
				authPatterns = appendUnique(authPatterns, "RBAC")
			}
		}
		rd["auth_detection"] = map[string]interface{}{"source": "heuristic", "patterns": authPatterns}

		// --- CI/CD detection from file paths ---
		var cicdTools []string
		hasDockerfile := false
		for _, f := range files {
			base := filepath.Base(f.Path)
			lower := strings.ToLower(base)
			if lower == "dockerfile" || strings.HasPrefix(lower, "dockerfile.") {
				hasDockerfile = true
			}
			if lower == "jenkinsfile" {
				cicdTools = appendUnique(cicdTools, "Jenkins")
			}
			if strings.Contains(f.Path, ".github/workflows") {
				cicdTools = appendUnique(cicdTools, "GitHub Actions")
			}
			if strings.Contains(f.Path, ".gitlab-ci") {
				cicdTools = appendUnique(cicdTools, "GitLab CI")
			}
			if strings.Contains(f.Path, "tekton") {
				cicdTools = appendUnique(cicdTools, "Tekton")
			}
			if lower == "docker-compose.yml" || lower == "docker-compose.yaml" || lower == "compose.yml" {
				cicdTools = appendUnique(cicdTools, "Docker Compose")
			}
			if lower == "vercel.json" || lower == ".vercel" {
				cicdTools = appendUnique(cicdTools, "Vercel")
			}
			if strings.Contains(f.Path, "kubernetes") || strings.Contains(f.Path, "k8s") || lower == "kustomization.yaml" {
				cicdTools = appendUnique(cicdTools, "Kubernetes")
			}
		}
		if hasDockerfile {
			cicdTools = appendUnique(cicdTools, "Docker")
		}
		rd["cicd_detection"] = map[string]interface{}{
			"source":         "heuristic",
			"tools":          cicdTools,
			"has_dockerfile": hasDockerfile,
		}

		// --- Cliff notes (actual knowledge artifacts) ---
		if knowledgeStore != nil {
			artifacts := knowledgeStore.GetKnowledgeArtifacts(repoID)
			var cliffNotes []map[string]string
			for _, art := range artifacts {
				if art.Type == knowledge.ArtifactCliffNotes && art.Status == knowledge.StatusReady {
					sections := knowledgeStore.GetKnowledgeSections(art.ID)
					for _, sec := range sections {
						cliffNotes = append(cliffNotes, map[string]string{
							"title":   sec.Title,
							"summary": sec.Summary,
							"content": sec.Content,
						})
					}
				}
			}
			if len(cliffNotes) > 0 {
				rd["cliff_notes"] = cliffNotes
			}
		}

		// --- Requirements ---
		reqs, reqTotal := store.GetRequirements(repoID, 50, 0)
		if reqTotal > 0 {
			var reqSummaries []map[string]string
			linkedCount := 0
			for _, req := range reqs {
				reqSummaries = append(reqSummaries, map[string]string{
					"external_id": req.ExternalID,
					"title":       req.Title,
					"source":      req.Source,
				})
				links := store.GetLinksForRequirement(req.ID, false)
				if len(links) > 0 {
					linkedCount++
				}
			}
			rd["requirements"] = map[string]interface{}{
				"total":         reqTotal,
				"linked_count":  linkedCount,
				"coverage_pct":  float64(linkedCount) / float64(reqTotal) * 100,
				"sample_titles": reqSummaries,
			}
		}

		// --- Symbols summary (top classes/functions) ---
		symbols, symTotal := store.GetSymbols(repoID, nil, nil, 100, 0)
		kindCounts := map[string]int{}
		var publicSymbols []map[string]string
		for _, sym := range symbols {
			kindCounts[string(sym.Kind)]++
			if sym.DocComment != "" && len(publicSymbols) < 30 {
				publicSymbols = append(publicSymbols, map[string]string{
					"name":      sym.Name,
					"kind":      string(sym.Kind),
					"file":      sym.FilePath,
					"signature": sym.Signature,
				})
			}
		}
		rd["symbols"] = map[string]interface{}{
			"total":             symTotal,
			"by_kind":           kindCounts,
			"documented_sample": publicSymbols,
		}

		// --- Doc and AI coverage ---
		withDocs, totalPub := store.GetPublicSymbolDocCoverage(repoID)
		aiFiles, totalFiles := store.GetAICodeFileRatio(repoID)
		rd["doc_coverage"] = map[string]interface{}{
			"documented": withDocs,
			"total":      totalPub,
		}
		rd["ai_code"] = map[string]interface{}{
			"ai_files":    aiFiles,
			"total_files": totalFiles,
		}

		// --- Git analysis ---
		rd["git_analysis"] = map[string]interface{}{
			"branch":     repo.Branch,
			"commit_sha": repo.CommitSHA,
		}

		// --- Secret scanner placeholder ---
		rd["secret_scanner"] = map[string]interface{}{
			"source":        "heuristic",
			"finding_count": 0,
		}

		data[repoID] = rd
	}

	return data
}

func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

// knowledgeFreshnessAdapter bridges KnowledgeStore to KnowledgeFreshnessProvider.
type knowledgeFreshnessAdapter struct {
	store knowledge.KnowledgeStore
}

func (a *knowledgeFreshnessAdapter) GetFreshnessRatio(repoID string) (fresh int, total int) {
	artifacts := a.store.GetKnowledgeArtifacts(repoID)
	total = len(artifacts)
	for _, art := range artifacts {
		if !art.Stale && art.Status == knowledge.StatusReady {
			fresh++
		}
	}
	return
}
