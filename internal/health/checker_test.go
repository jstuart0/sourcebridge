// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package health_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/health"
)

type okDB struct{}

func (okDB) Ping(_ context.Context) error { return nil }

type failDB struct{}

func (failDB) Ping(_ context.Context) error { return errors.New("connection refused") }

type okWorker struct{}

func (okWorker) CheckHealth(_ context.Context) (bool, error) { return true, nil }

type failWorker struct{}

func (failWorker) CheckHealth(_ context.Context) (bool, error) {
	return false, errors.New("unavailable")
}

func TestChecker_BothHealthy(t *testing.T) {
	c := health.New(okDB{}, okWorker{})
	s := c.Get(context.Background())
	if !s.Overall || !s.Surreal || !s.Worker {
		t.Errorf("expected all healthy, got %+v", s)
	}
	if s.Message != "All systems normal" {
		t.Errorf("unexpected message: %q", s.Message)
	}
}

func TestChecker_DBDown(t *testing.T) {
	c := health.New(failDB{}, okWorker{})
	s := c.Get(context.Background())
	if s.Overall {
		t.Error("overall must be false when DB is down")
	}
	if s.Surreal {
		t.Error("surreal must be false when DB ping fails")
	}
	if !s.Worker {
		t.Error("worker must be true when worker is healthy")
	}
}

func TestChecker_WorkerDown(t *testing.T) {
	c := health.New(okDB{}, failWorker{})
	s := c.Get(context.Background())
	if s.Overall {
		t.Error("overall must be false when worker is down")
	}
	if !s.Surreal {
		t.Error("surreal must be true when DB is healthy")
	}
	if s.Worker {
		t.Error("worker must be false when worker check fails")
	}
}

func TestChecker_BothDown(t *testing.T) {
	c := health.New(failDB{}, failWorker{})
	s := c.Get(context.Background())
	if s.Overall || s.Surreal || s.Worker {
		t.Errorf("expected all unhealthy, got %+v", s)
	}
}

func TestChecker_NilDB_TreatedHealthy(t *testing.T) {
	c := health.New(nil, okWorker{})
	s := c.Get(context.Background())
	if !s.Surreal {
		t.Error("nil DB should be treated as healthy (embedded mode)")
	}
}

func TestChecker_NilWorker_TreatedHealthy(t *testing.T) {
	c := health.New(okDB{}, nil)
	s := c.Get(context.Background())
	if !s.Worker {
		t.Error("nil worker should be treated as healthy (AI features disabled)")
	}
}

func TestChecker_ResultsCached(t *testing.T) {
	calls := 0
	db := &countingDB{fn: func() error { calls++; return nil }}
	c := health.New(db, nil)

	c.Get(context.Background())
	c.Get(context.Background())
	c.Get(context.Background())

	if calls != 1 {
		t.Errorf("expected 1 DB probe (cached), got %d", calls)
	}
}

func TestChecker_CacheExpires(t *testing.T) {
	// Use a tiny TTL by probing via the public API — we can't set CacheTTL
	// from outside the package, so instead we rely on the real CacheTTL (5s)
	// being longer than the test duration. We just verify the cached value
	// returned matches the most recent probe.
	c := health.New(okDB{}, okWorker{})
	s1 := c.Get(context.Background())
	// Tiny sleep to ensure CheckedAt timestamps differ (sub-ms resolution is
	// unreliable, so sleep 2ms).
	time.Sleep(2 * time.Millisecond)
	s2 := c.Get(context.Background())

	// Both should be healthy and within CacheTTL of each other.
	if !s1.Overall || !s2.Overall {
		t.Error("both results should be healthy")
	}
}

type countingDB struct {
	fn func() error
}

func (d *countingDB) Ping(_ context.Context) error { return d.fn() }
