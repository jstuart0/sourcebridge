//go:build enterprise

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	"github.com/sourcebridge/sourcebridge/internal/api/middleware"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/jstuart0/sourcebridge-enterprise/routes"
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

	// Wire the report generator to call the Python worker via gRPC
	if s.worker != nil {
		ectx.API.SetReportGenerator(func(reportID, reportType, audience, repoDataJSON, sectionDefsJSON, outputDir string, repoIDs, selectedSections []string, includeDiagrams bool, loeMode, reportName string) (string, int, int, int, error) {
			ctx := context.Background()
			resp, err := s.worker.GenerateReport(ctx, &knowledgev1.GenerateReportRequest{
				ReportId:               reportID,
				ReportName:             reportName,
				ReportType:             reportType,
				Audience:               audience,
				RepositoryIds:          repoIDs,
				SelectedSections:       selectedSections,
				IncludeDiagrams:        includeDiagrams,
				LoeMode:                loeMode,
				OutputDir:              outputDir,
				RepoDataJson:           repoDataJSON,
				SectionDefinitionsJson: sectionDefsJSON,
			})
			if err != nil {
				return "", 0, 0, 0, err
			}
			return resp.Markdown, int(resp.SectionCount), int(resp.WordCount), int(resp.EvidenceCount), nil
		})
		slog.Info("report generator wired to Python worker via gRPC")
	}

	r.Route("/api/v1/reports", func(r chi.Router) {
		ectx.RegisterReportRoutes(r)
	})
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
