// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package trash

// Retention worker — hard-deletes tombstoned rows older than the
// configured retention window. Runs in a loop on every replica; a
// Redis-backed leader lock (db.Cache.SetIfAbsent) ensures only one
// replica actually sweeps per tick. If no Redis is configured, the
// worker degrades to "every replica sweeps" which is safe because the
// sweep is idempotent but wastes cycles; the warn at startup tells
// operators to configure Redis for production.

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// WorkerConfig controls the retention worker's cadence.
type WorkerConfig struct {
	RetentionDays int
	SweepInterval time.Duration
	MaxBatchSize  int
}

// Normalize returns a WorkerConfig with sensible defaults applied.
// Callers can pass zero values and get 30-day retention, 6-hour sweep,
// 500-row batches.
func (c WorkerConfig) Normalize() WorkerConfig {
	if c.RetentionDays <= 0 {
		c.RetentionDays = 30
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 6 * time.Hour
	}
	if c.MaxBatchSize <= 0 {
		c.MaxBatchSize = 500
	}
	return c
}

// Worker manages the retention sweep goroutine.
type Worker struct {
	store  Store
	cache  db.Cache
	cfg    WorkerConfig
	lockID string

	stopped atomic.Bool
}

// NewWorker constructs a worker. Pass nil for cache to run in
// degraded "all-replicas-sweep" mode; otherwise cache is used for
// Redis leader election.
func NewWorker(store Store, cache db.Cache, cfg WorkerConfig) *Worker {
	host, _ := os.Hostname()
	pid := os.Getpid()
	return &Worker{
		store:  store,
		cache:  cache,
		cfg:    cfg.Normalize(),
		lockID: host + "/" + itoa(pid),
	}
}

func itoa(n int) string {
	// Tiny local itoa to avoid importing strconv for one call.
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	buf := make([]byte, 0, 10)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{digits[n%10]}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// Run blocks until ctx is cancelled, invoking a sweep at each tick.
// Returns nil on graceful shutdown. The worker never returns an
// error — individual sweep failures are logged and the loop
// continues.
func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil {
		slog.Warn("trash retention worker started with nil store; returning immediately")
		return nil
	}
	degraded := w.cache == nil
	if degraded {
		slog.Warn("trash retention worker running in degraded mode (no cache/lock available) — each replica will sweep independently; configure Redis for production")
	}

	ticker := time.NewTicker(w.cfg.SweepInterval)
	defer ticker.Stop()

	// Run a sweep on boot so operators don't wait sweep_interval for
	// the first purge after enabling the feature.
	w.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			w.stopped.Store(true)
			return nil
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick runs exactly one sweep, coordinating with peers via Redis
// when available.
func (w *Worker) tick(ctx context.Context) {
	if w.cache != nil {
		// Leader election: the first replica to claim the lock for
		// (sweep_interval + 1 min) wins this tick. Ttl exceeds sweep
		// interval so the lock outlives a normal sweep. If the
		// leader crashes, the TTL releases the lock for the next
		// tick.
		ttl := w.cfg.SweepInterval + time.Minute
		got, err := w.cache.SetIfAbsent(ctx, "trash:sweep:lock", w.lockID, ttl)
		if err != nil {
			slog.Warn("trash sweep leader-election failed; skipping this tick", "error", err)
			return
		}
		if !got {
			slog.Debug("trash_sweep_skipped_not_leader", "holder_known_as", w.lockID)
			return
		}
	}

	retention := time.Duration(w.cfg.RetentionDays) * 24 * time.Hour
	start := time.Now()
	slog.Info("trash_sweep_started", "retention_days", w.cfg.RetentionDays, "leader", w.lockID)
	purged, err := w.store.SweepExpired(ctx, retention, w.cfg.MaxBatchSize)
	if err != nil {
		slog.Error("trash_sweep_failed", "error", err, "purged_before_error", purged)
		return
	}
	if purged > 0 {
		PurgesTotal.Add(int64(purged))
	}
	slog.Info("trash_sweep_completed",
		"purged", purged,
		"duration_ms", time.Since(start).Milliseconds())
}

// Stopped reports whether the worker has finished. Useful for tests
// that want to verify shutdown happened cleanly.
func (w *Worker) Stopped() bool { return w.stopped.Load() }
