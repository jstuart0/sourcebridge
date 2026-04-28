// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package health provides the shared platform health checker used by both the
// /readyz HTTP handler and the serviceHealth GraphQL query. Keeping it in its
// own package avoids a circular import between internal/api/rest (which imports
// internal/api/graphql) and internal/api/graphql.
package health

import (
	"context"
	"sync"
	"time"
)

// CacheTTL is how long a check result is reused before re-probing the DB and
// worker. A 5-second window absorbs a hot UI poll (every 15 s) without
// meaningfully delaying outage detection.
const CacheTTL = 5 * time.Second

// CheckTimeout is the per-subsystem probe deadline. Kept tight so a single
// dead dependency doesn't stall /readyz past the Kubernetes readiness probe
// timeout.
const CheckTimeout = 1 * time.Second

// DBPinger is the narrow interface the Checker needs from SurrealDB.
// *db.SurrealDB satisfies this via its Ping method.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// WorkerChecker is the narrow interface the Checker needs from the gRPC
// worker client. *worker.Client satisfies this via CheckHealth.
type WorkerChecker interface {
	CheckHealth(ctx context.Context) (bool, error)
}

// Status is the result of a single health probe cycle.
type Status struct {
	Overall   bool
	Surreal   bool
	Worker    bool
	Message   string
	CheckedAt time.Time
}

// Checker probes SurrealDB and the gRPC worker, caches the result for
// CacheTTL, and exposes a single Get method shared by the /readyz handler
// and the serviceHealth GraphQL resolver.
type Checker struct {
	db     DBPinger
	worker WorkerChecker

	mu        sync.Mutex
	cached    *Status
	cacheTime time.Time
}

// New constructs a Checker. Either dependency may be nil — a nil DB means
// embedded/in-memory mode (considered healthy), a nil worker means AI features
// are disabled (also considered healthy so the banner stays hidden).
func New(db DBPinger, worker WorkerChecker) *Checker {
	return &Checker{db: db, worker: worker}
}

// Get returns a cached Status when one is fresher than CacheTTL, otherwise
// probes each subsystem synchronously and caches the new result.
func (c *Checker) Get(ctx context.Context) Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cached != nil && time.Since(c.cacheTime) < CacheTTL {
		return *c.cached
	}

	s := c.probe(ctx)
	c.cached = &s
	c.cacheTime = time.Now()
	return s
}

func (c *Checker) probe(ctx context.Context) Status {
	surreal := c.probeDB(ctx)
	worker := c.probeWorker(ctx)

	overall := surreal && worker
	msg := "All systems normal"
	switch {
	case !surreal && !worker:
		msg = "SurrealDB unreachable; AI worker unreachable"
	case !surreal:
		msg = "SurrealDB unreachable"
	case !worker:
		msg = "AI worker unreachable"
	}

	return Status{
		Overall:   overall,
		Surreal:   surreal,
		Worker:    worker,
		Message:   msg,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) probeDB(ctx context.Context) bool {
	if c.db == nil {
		// Embedded/in-memory mode — no live DB to ping.
		return true
	}
	pingCtx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	return c.db.Ping(pingCtx) == nil
}

func (c *Checker) probeWorker(ctx context.Context) bool {
	if c.worker == nil {
		// Worker not configured — treat as healthy so the banner stays hidden
		// when AI features are intentionally disabled.
		return true
	}
	probeCtx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	ok, err := c.worker.CheckHealth(probeCtx)
	return err == nil && ok
}
