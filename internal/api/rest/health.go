// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// componentStatus represents the health state of a single dependency.
type componentStatus struct {
	Status string `json:"status"` // healthy, degraded, unavailable
	Detail string `json:"detail,omitempty"`
}

// readinessResponse is the structured response for /readyz.
type readinessResponse struct {
	Status     string                     `json:"status"` // ready, degraded, unavailable
	Components map[string]componentStatus `json:"components"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	resp := readinessResponse{
		Status:     "ready",
		Components: make(map[string]componentStatus),
	}

	// API is always healthy if we're serving this request
	resp.Components["api"] = componentStatus{Status: "healthy"}

	// Database: mandatory
	dbStatus := componentStatus{Status: "healthy"}
	if s.cfg.Storage.SurrealMode == "external" {
		// For external mode, the store was already initialized at startup.
		// A deeper check would ping SurrealDB, but the store being non-nil
		// confirms the connection was established.
		if s.store == nil {
			dbStatus = componentStatus{Status: "unavailable", Detail: "store not initialized"}
			resp.Status = "unavailable"
		}
	} else {
		dbStatus = componentStatus{Status: "healthy", Detail: "embedded/in-memory"}
	}
	resp.Components["database"] = dbStatus

	// Worker: optional for core readiness
	workerStatus := componentStatus{Status: "unavailable", Detail: "not configured"}
	if s.worker != nil {
		healthy, err := s.worker.CheckHealth(context.Background())
		if err != nil {
			workerStatus = componentStatus{Status: "unavailable", Detail: err.Error()}
		} else if healthy {
			workerStatus = componentStatus{Status: "healthy"}
		} else {
			workerStatus = componentStatus{Status: "degraded", Detail: "not serving"}
		}
	}
	resp.Components["worker"] = workerStatus

	// Determine overall status: worker degradation doesn't make core unavailable
	httpStatus := http.StatusOK
	if resp.Status == "unavailable" {
		httpStatus = http.StatusServiceUnavailable
	} else if workerStatus.Status != "healthy" {
		resp.Status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(resp)
}

// --- Prometheus metrics ---

// metrics tracks basic operational counters.
var metrics = struct {
	httpRequestsTotal   atomic.Int64
	httpRequestDuration atomic.Int64 // total microseconds
	gqlOperationsTotal  atomic.Int64
	workerRPCTotal      atomic.Int64
	workerRPCErrors     atomic.Int64
	indexingTotal       atomic.Int64
}{}

// metricsMiddleware increments request counters.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		metrics.httpRequestsTotal.Add(1)
		next.ServeHTTP(w, r)
		dur := time.Since(start).Microseconds()
		metrics.httpRequestDuration.Add(dur)
	})
}

// graphqlCountMiddleware increments the gql operation counter.
func graphqlCountMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.gqlOperationsTotal.Add(1)
		next.ServeHTTP(w, r)
	})
}

// rateLimitState tracks concurrent AI operations.
var aiConcurrency struct {
	mu      sync.Mutex
	current int
	limit   int
}

func init() {
	aiConcurrency.limit = 5 // max concurrent AI GraphQL operations
}

// aiConcurrencyMiddleware rejects requests when concurrent AI operations exceed the limit.
func aiConcurrencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiConcurrency.mu.Lock()
		if aiConcurrency.current >= aiConcurrency.limit {
			aiConcurrency.mu.Unlock()
			http.Error(w, `{"errors":[{"message":"AI operations at capacity, try again shortly"}]}`, http.StatusTooManyRequests)
			return
		}
		aiConcurrency.current++
		aiConcurrency.mu.Unlock()
		defer func() {
			aiConcurrency.mu.Lock()
			aiConcurrency.current--
			aiConcurrency.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	reqTotal := metrics.httpRequestsTotal.Load()
	reqDurTotal := metrics.httpRequestDuration.Load()
	gqlTotal := metrics.gqlOperationsTotal.Load()
	workerTotal := metrics.workerRPCTotal.Load()
	workerErrors := metrics.workerRPCErrors.Load()
	indexTotal := metrics.indexingTotal.Load()
	activePoolSize := 0
	configuredPoolSize := 0
	if s != nil && s.orchestrator != nil {
		activePoolSize = s.orchestrator.ActiveWorkerCount()
		configuredPoolSize = s.orchestrator.MaxConcurrency()
	}
	runtimeReconfigureEnabled := 0
	if s != nil && s.flags.RuntimeReconfigure {
		runtimeReconfigureEnabled = 1
	}

	up := 1
	fmt.Fprintf(w, "# HELP sourcebridge_up Whether the service is up\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_up gauge\n")
	fmt.Fprintf(w, "sourcebridge_up %d\n", up)

	fmt.Fprintf(w, "# HELP sourcebridge_http_requests_total Total HTTP requests\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_http_requests_total counter\n")
	fmt.Fprintf(w, "sourcebridge_http_requests_total %d\n", reqTotal)

	fmt.Fprintf(w, "# HELP sourcebridge_http_request_duration_microseconds_total Total HTTP request duration\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_http_request_duration_microseconds_total counter\n")
	fmt.Fprintf(w, "sourcebridge_http_request_duration_microseconds_total %d\n", reqDurTotal)

	fmt.Fprintf(w, "# HELP sourcebridge_graphql_operations_total Total GraphQL operations\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_graphql_operations_total counter\n")
	fmt.Fprintf(w, "sourcebridge_graphql_operations_total %d\n", gqlTotal)

	fmt.Fprintf(w, "# HELP sourcebridge_worker_rpc_total Total worker RPC calls\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_worker_rpc_total counter\n")
	fmt.Fprintf(w, "sourcebridge_worker_rpc_total %d\n", workerTotal)

	fmt.Fprintf(w, "# HELP sourcebridge_worker_rpc_errors_total Total worker RPC errors\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_worker_rpc_errors_total counter\n")
	fmt.Fprintf(w, "sourcebridge_worker_rpc_errors_total %d\n", workerErrors)

	fmt.Fprintf(w, "# HELP sourcebridge_indexing_total Total repository indexing operations\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_indexing_total counter\n")
	fmt.Fprintf(w, "sourcebridge_indexing_total %d\n", indexTotal)

	fmt.Fprintf(w, "# HELP sourcebridge_llm_orchestrator_active_pool_size Active worker goroutines in the orchestrator pool\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_llm_orchestrator_active_pool_size gauge\n")
	fmt.Fprintf(w, "sourcebridge_llm_orchestrator_active_pool_size %d\n", activePoolSize)

	fmt.Fprintf(w, "# HELP sourcebridge_llm_orchestrator_configured_pool_size Desired worker goroutine count in the orchestrator pool\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_llm_orchestrator_configured_pool_size gauge\n")
	fmt.Fprintf(w, "sourcebridge_llm_orchestrator_configured_pool_size %d\n", configuredPoolSize)

	fmt.Fprintf(w, "# HELP sourcebridge_feature_runtime_reconfigure_enabled Whether runtime orchestrator reconfiguration is enabled\n")
	fmt.Fprintf(w, "# TYPE sourcebridge_feature_runtime_reconfigure_enabled gauge\n")
	fmt.Fprintf(w, "sourcebridge_feature_runtime_reconfigure_enabled %d\n", runtimeReconfigureEnabled)
}
