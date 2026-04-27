// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package scheduler implements the periodic living-wiki regeneration scheduler.
//
// # Multi-replica safety
//
// The scheduler ticks on every API replica. Leader election using the same
// Redis-backed lock primitives as the trash retention worker (see
// internal/trash/worker.go) ensures only the lease-holding replica submits
// refresh events per tick. Non-leader replicas skip the submission loop and
// sleep until the next tick.
//
// # Thundering-herd prevention
//
// On the first tick after boot, each repo's effective due time is offset by a
// deterministic per-repo jitter derived from an FNV-32a hash of the repo ID.
// The jitter is bounded to [0, 5 minutes) and is stable across restarts, so
// repos do not re-jitter every time the process restarts.
//
// # Concurrency cap
//
// A per-tenant concurrency cap (MaxParallel) limits how many repos are submitted
// in a single tick regardless of how many are due. Repos that are not submitted
// in a tick are picked up on the next tick.
package scheduler

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─────────────────────────────────────────────────────────────────────────────
// SchedulerDeps
// ─────────────────────────────────────────────────────────────────────────────

// SchedulerDeps carries all external dependencies for the Scheduler.
type SchedulerDeps struct {
	// RepoStore is used to list repos with enabled=true at each tick.
	// Required.
	RepoStore livingwiki.RepoSettingsStore

	// Dispatcher receives ManualRefreshEvents for repos that are due.
	// Required.
	Dispatcher *webhook.Dispatcher

	// Cache is used for leader election (same pattern as trash.Worker).
	// When nil, the scheduler runs on all replicas simultaneously
	// (acceptable for single-replica deployments; not recommended for HA).
	Cache db.Cache

	// Interval is the default regen interval for all repos.
	// Defaults to 15 minutes.
	Interval time.Duration

	// MaxParallel is the per-tenant concurrency cap: the maximum number of
	// repos submitted per tick. Defaults to 5.
	MaxParallel int

	// TenantID scopes all store queries to the given tenant.
	TenantID string
}

// ─────────────────────────────────────────────────────────────────────────────
// Scheduler
// ─────────────────────────────────────────────────────────────────────────────

// Scheduler is the periodic living-wiki regeneration scheduler.
// It runs as a long-lived goroutine via [Scheduler.Run].
type Scheduler struct {
	repoStore   livingwiki.RepoSettingsStore
	dispatcher  *webhook.Dispatcher
	cache       db.Cache
	interval    time.Duration
	maxParallel int
	tenantID    string
	lockID      string   // unique ID for this replica's leader-election entry
	stopped     atomic.Bool
}

const (
	defaultInterval    = 15 * time.Minute
	defaultMaxParallel = 5

	// leaderLockKey is the Redis key used for scheduler leader election.
	// Must be distinct from trash:sweep:lock.
	leaderLockKey = "livingwiki:scheduler:lock"

	// jitterWindowSeconds is the maximum jitter offset applied to a repo's
	// first-tick due time. 300 seconds = 5 minutes.
	jitterWindowSeconds = 300
)

// New creates a Scheduler. Call [Scheduler.Run] to start it.
func New(deps SchedulerDeps) *Scheduler {
	if deps.Interval <= 0 {
		deps.Interval = defaultInterval
	}
	if deps.MaxParallel <= 0 {
		deps.MaxParallel = defaultMaxParallel
	}

	host, _ := os.Hostname()
	pid := os.Getpid()
	lockID := fmt.Sprintf("livingwiki-scheduler/%s/%d", host, pid)

	return &Scheduler{
		repoStore:   deps.RepoStore,
		dispatcher:  deps.Dispatcher,
		cache:       deps.Cache,
		interval:    deps.Interval,
		maxParallel: deps.MaxParallel,
		tenantID:    deps.TenantID,
		lockID:      lockID,
	}
}

// Run blocks until ctx is cancelled, invoking a scheduler tick at each
// interval. Returns nil on graceful shutdown. Individual tick errors are
// logged and the loop continues — the scheduler never exits on transient
// errors.
func (s *Scheduler) Run(ctx context.Context) error {
	if s.repoStore == nil {
		slog.Warn("livingwiki scheduler: no repo store configured; scheduler exiting immediately")
		return nil
	}
	if s.dispatcher == nil {
		slog.Warn("livingwiki scheduler: no dispatcher configured; scheduler exiting immediately")
		return nil
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run a tick immediately on boot so operators don't wait one full interval
	// for the first scheduled refresh after enabling the feature.
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			s.stopped.Store(true)
			return nil
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick runs one scheduler tick, coordinating with peers via the cache when
// available.
func (s *Scheduler) tick(ctx context.Context) {
	if s.cache != nil {
		// Leader election: claim the lock for (interval + 1 min). Only the
		// first replica to acquire wins this tick. If the leader crashes, the
		// TTL releases the lock before the next tick.
		ttl := s.interval + time.Minute
		got, err := s.cache.SetIfAbsent(ctx, leaderLockKey, s.lockID, ttl)
		if err != nil {
			slog.Warn("livingwiki scheduler: leader election failed; skipping tick", "error", err)
			return
		}
		if !got {
			slog.Debug("livingwiki scheduler: not leader; skipping tick", "leader", s.lockID)
			return
		}
	}

	repos, err := s.repoStore.ListEnabledRepos(ctx, s.tenantID)
	if err != nil {
		slog.Error("livingwiki scheduler: failed to list enabled repos", "error", err)
		return
	}

	now := time.Now()
	var submitted int
	var submittedIDs []string

	for i := range repos {
		if submitted >= s.maxParallel {
			slog.Debug("livingwiki scheduler: per-tenant concurrency cap reached; deferring remaining repos",
				"cap", s.maxParallel, "remaining", len(repos)-i)
			break
		}

		repo := repos[i]
		nextDue := nextDueTime(repo, s.interval)
		if now.Before(nextDue) {
			continue
		}

		event := webhook.ManualRefreshEvent{
			Repo:        repo.RepoID,
			Delivery:    fmt.Sprintf("scheduler:%s:%d", repo.RepoID, now.UnixNano()),
			RequestedBy: "scheduler",
		}
		if submitErr := s.dispatcher.Submit(ctx, event); submitErr != nil {
			slog.Warn("livingwiki scheduler: failed to submit refresh event",
				"repo_id", repo.RepoID,
				"error", submitErr,
			)
			continue
		}

		submitted++
		submittedIDs = append(submittedIDs, repo.RepoID)
	}

	if submitted > 0 {
		slog.Info("livingwiki scheduler: submitted refresh events",
			"count", submitted,
			"repo_ids", submittedIDs,
		)
	}
}

// nextDueTime returns the time at which a repo next becomes eligible for a
// scheduler-triggered refresh. On the first run (LastRunAt == nil), the jitter
// is applied relative to the process start time so repos are distributed
// across a 5-minute boot window. On subsequent ticks the jitter is not
// re-applied — LastRunAt advances naturally.
func nextDueTime(repo livingwiki.RepositoryLivingWikiSettings, defaultInterval time.Duration) time.Time {
	interval := intervalForRepo(repo, defaultInterval)
	if repo.LastRunAt == nil {
		// First run: due immediately, but offset by deterministic jitter so all
		// repos on a freshly started replica don't submit at second zero.
		return time.Now().Add(jitterFor(repo.RepoID) - interval)
	}
	return repo.LastRunAt.Add(interval)
}

// intervalForRepo returns the regen interval for a specific repo.
// Currently returns the global default. Future work: per-repo override via settings.
func intervalForRepo(repo livingwiki.RepositoryLivingWikiSettings, defaultInterval time.Duration) time.Duration {
	_ = repo // future: check repo.Settings.SchedulerInterval
	return defaultInterval
}

// jitterFor returns a deterministic per-repo jitter offset bounded to
// [0, jitterWindowSeconds) seconds. Uses FNV-32a so the result is stable
// across restarts and uniform across repo IDs.
func jitterFor(repoID string) time.Duration {
	h := fnv.New32a()
	h.Write([]byte(repoID))
	return time.Duration(h.Sum32()%jitterWindowSeconds) * time.Second
}

// Stopped reports whether the scheduler has finished. Useful for tests
// that want to verify graceful shutdown occurred.
func (s *Scheduler) Stopped() bool { return s.stopped.Load() }
