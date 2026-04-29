// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package assembly is the single construction point for the living-wiki runtime.
// It wires all orchestrator ports and returns a ready-to-start Dispatcher.
//
// # Ownership
//
// cli/serve.go calls [AssembleDispatcher] once. All living-wiki boot logic
// lives here. Future port additions touch only this file, not cli/serve.go.
//
// # Kill-switch
//
// The caller (cli/serve.go) is responsible for checking the kill-switch env var
// before calling AssembleDispatcher. When the kill-switch is active the caller
// keeps the dispatcher nil and registers only the disabled stub routes.
package assembly

import (
	"context"
	"fmt"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/governance"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// AssemblerDeps carries every external dependency that the assembly factory
// needs. Fields that are nil result in reduced functionality:
//   - WorkerClient nil → LLM-dependent template generation is unavailable; the
//     Dispatcher starts but Generate calls that reach LLM-required templates
//     will return an error (pages are excluded, not a panic).
//   - GraphStore nil → GraphMetricsProvider is replaced with ConstGraphMetrics{0,0}.
//     The architectural_relevance validator may exclude pages that would pass
//     with a real store.
type AssemblerDeps struct {
	// SurrealDB is the connected SurrealDB client. Required for PageStore and
	// WatermarkStore. When nil, AssembleDispatcher returns an error.
	SurrealDB *db.SurrealDB

	// GraphStore supplies symbol data for GraphMetricsProvider. Optional.
	GraphStore graph.GraphStore

	// WorkerClient is the gRPC reasoning worker. Optional; when nil the LLM
	// caller returns an error, causing template generation to fail gracefully.
	//
	// Slice 2 of the workspace-LLM-source-of-truth plan: the assembled LLM
	// caller goes through LLMCaller (an llmcall.Caller) so workspace-saved
	// settings are attached to gRPC metadata. WorkerClient is kept for
	// other ports that need direct access (none today; placeholder).
	WorkerClient *worker.Client

	// LLMCaller is the LLM-aware adapter around WorkerClient. Required for
	// LLM-bearing template generation; when nil the assembled
	// templates.LLMCaller returns errors and LLM-dependent pages are
	// excluded gracefully.
	LLMCaller *llmcall.Caller

	// Broker supplies credentials per-call. Required (R3 workstream).
	Broker credentials.Broker

	// Logger receives structured log lines from the Dispatcher. When nil,
	// webhook.NoopLogger is used.
	Logger webhook.Logger

	// WorkerCount is the number of overflow-queue worker goroutines. Defaults
	// to 4 when ≤ 0.
	WorkerCount int

	// EventTimeout bounds each event handler. Defaults to 5 minutes when zero.
	EventTimeout time.Duration
}

// AssembleDispatcher constructs and wires all living-wiki runtime dependencies,
// returning a Dispatcher ready for [webhook.Dispatcher.Start].
//
// Port wiring table:
//
//	PageStore          → db.NewLivingWikiPageStore (SurrealDB-backed)
//	WatermarkStore     → db.NewLivingWikiWatermarkStore (SurrealDB-backed)
//	TemplateRegistry   → orchestrator.NewDefaultRegistry (built-in templates)
//	GraphMetrics       → orchestrator.NewGraphStoreMetrics when GraphStore is set
//	LLMCaller          → workerLLMCaller backed by WorkerClient.AnswerQuestion
//	WikiPR             → nil (TODO(R6): per-job snapshot from broker)
//	RepoWriter         → nil (TODO(R6): per-job snapshot from broker)
//	DiffProvider       → nil (OSS path; enterprise injects via ServerOption)
//	SinkEditConfig.AuditLog      → noopAuditLog (TODO(R8): SurrealDB-backed)
//	SinkEditConfig.OverlayStore  → orchestrator.NewMemorySinkOverlayStore
//	SinkEditConfig.SyncPRs       → orchestrator.NewMemorySyncPRStore
//
// Any error constructing a required port causes AssembleDispatcher to return
// an error. The caller (cli/serve.go) logs the error and continues with
// lwDispatcher = nil so the server still starts without living-wiki.
func AssembleDispatcher(deps AssemblerDeps) (*webhook.Dispatcher, error) {
	if deps.SurrealDB == nil {
		return nil, fmt.Errorf("assembly: SurrealDB is required")
	}
	if deps.Broker == nil {
		return nil, fmt.Errorf("assembly: Broker is required")
	}

	// ── PageStore ─────────────────────────────────────────────────────────────
	pageStore := db.NewLivingWikiPageStore(deps.SurrealDB)

	// ── WatermarkStore ────────────────────────────────────────────────────────
	watermarkStore := db.NewLivingWikiWatermarkStore(deps.SurrealDB)

	// ── LLMCaller ─────────────────────────────────────────────────────────────
	var llmCaller templates.LLMCaller
	if deps.LLMCaller != nil && deps.LLMCaller.IsAvailable() {
		llmCaller = &workerLLMCaller{caller: deps.LLMCaller}
	} else {
		llmCaller = &noopLLMCaller{}
	}
	_ = llmCaller // threaded through GenerateInput at job-dispatch time, not here

	// ── TemplateRegistry ──────────────────────────────────────────────────────
	// Uses orchestrator.NewDefaultRegistry which contains all built-in A1.P1
	// templates: architecture, api_reference, system_overview, glossary.
	registry := orchestrator.NewDefaultRegistry()

	// ── GraphMetricsProvider ──────────────────────────────────────────────────
	var graphMetrics orchestrator.GraphMetricsProvider
	if deps.GraphStore != nil {
		graphMetrics = orchestrator.NewGraphStoreMetrics(deps.GraphStore)
	} else {
		graphMetrics = orchestrator.ConstGraphMetrics{}
	}

	// ── Orchestrator ──────────────────────────────────────────────────────────
	orch := orchestrator.New(
		orchestrator.Config{
			GraphMetrics: graphMetrics,
		},
		registry,
		pageStore,
	)

	// ── SinkEditConfig ────────────────────────────────────────────────────────
	// TODO(R8): replace AuditLog, OverlayStore, SyncPRs with SurrealDB-backed
	// implementations when the observability workstream ships.
	sinkEditCfg := orchestrator.SinkEditConfig{
		AuditLog:     &noopAuditLog{},
		OverlayStore: orchestrator.NewMemorySinkOverlayStore(),
		SyncPRs:      orchestrator.NewMemorySyncPRStore(),
		SinkConfigs:  map[ast.SinkName]governance.SinkConfig{},
	}

	// ── DispatcherDeps ────────────────────────────────────────────────────────
	dispatcherDeps := webhook.DispatcherDeps{
		Orchestrator:   orch,
		WatermarkStore: watermarkStore,
		SinkEditConfig: sinkEditCfg,
		// DiffProvider is nil (OSS path). Enterprise injects via a ServerOption.
		PushDiffProvider: nil,
		// SinkPollers wired by R6 when the scheduler ships.
		SinkPollers: nil, // TODO(R6)
		Logger:      deps.Logger,
	}

	// ── DispatcherConfig ──────────────────────────────────────────────────────
	workerCount := deps.WorkerCount
	if workerCount <= 0 {
		workerCount = 4
	}
	eventTimeout := deps.EventTimeout
	if eventTimeout <= 0 {
		eventTimeout = 5 * time.Minute
	}
	dispatcherCfg := webhook.DispatcherConfig{
		WorkerCount:  workerCount,
		EventTimeout: eventTimeout,
	}

	dispatcher := webhook.NewDispatcher(dispatcherDeps, dispatcherCfg)
	return dispatcher, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// workerLLMCaller
// ─────────────────────────────────────────────────────────────────────────────

// workerLLMCaller implements [templates.LLMCaller] backed by the gRPC reasoning
// worker. Slice 2 of the workspace-LLM-source-of-truth plan replaced the prior
// direct *worker.Client wiring with an llmcall.Caller wrapper so workspace-
// saved settings flow through gRPC metadata on every call.
type workerLLMCaller struct {
	caller *llmcall.Caller
}

func (w *workerLLMCaller) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if w.caller == nil {
		return "", fmt.Errorf("assembly: workerLLMCaller: llmcall.Caller is nil")
	}
	// The AnswerQuestion RPC takes a single Question string. We combine the
	// system-level instructions and the user-turn content so the reasoning
	// worker receives a complete, self-contained prompt.
	question := userPrompt
	if systemPrompt != "" {
		question = systemPrompt + "\n\n" + userPrompt
	}
	// repoID is empty here: the assembly-time caller does not know the
	// per-repo identity; the dispatcher's job context drives that. For
	// the static template-generation path that uses this caller, the
	// resolver falls through to workspace settings (env-bootstrap
	// fallback) and per-repo override is by definition not relevant.
	resp, err := w.caller.AnswerQuestion(ctx, "", resolution.OpLivingWikiAssembly, &reasoningv1.AnswerQuestionRequest{
		Question: question,
	})
	if err != nil {
		return "", fmt.Errorf("assembly: workerLLMCaller: %w", err)
	}
	return resp.GetAnswer(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// noopLLMCaller
// ─────────────────────────────────────────────────────────────────────────────

// noopLLMCaller is a stub [templates.LLMCaller] used when no worker is
// configured. It returns an error so the orchestrator excludes LLM-dependent
// pages rather than generating empty content.
type noopLLMCaller struct{}

func (n *noopLLMCaller) Complete(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("assembly: LLM worker not configured; LLM-dependent page generation unavailable")
}

// ─────────────────────────────────────────────────────────────────────────────
// noopAuditLog
// ─────────────────────────────────────────────────────────────────────────────

// noopAuditLog is a no-op [governance.AuditLog] used until R8 ships the real
// SurrealDB-backed persistence layer.
//
// TODO(R8): replace with a SurrealDB-backed implementation.
type noopAuditLog struct{}

func (n *noopAuditLog) Append(_ context.Context, _ governance.AuditEntry) error { return nil }
func (n *noopAuditLog) Query(_ context.Context, _ governance.AuditFilter) ([]governance.AuditEntry, error) {
	return nil, nil
}
