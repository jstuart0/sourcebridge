// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/version"
)

// versionLookup is a small concurrency-safe cache for the worker's
// reported version. /api/v1/version is intentionally public and may
// be hit at high cadence (sidebar component on every page navigation),
// so we never want to flood the worker's gRPC port with version
// probes. Mutex-protected because the critical section is bounded by
// the 250ms probe timeout — this is not a hot path that warrants
// atomic.Pointer or singleflight machinery.
type versionLookup struct {
	mu      sync.Mutex
	value   string
	expires time.Time
	ttl     time.Duration
	probe   func(ctx context.Context) (string, error) // nil → always returns ""
}

// newVersionLookup constructs a cache with the given TTL and probe
// function. Pass probe=nil before phase 3 wires GetWorkerVersion; the
// REST/GraphQL surfaces will simply return "" for workerVersion.
func newVersionLookup(ttl time.Duration, probe func(ctx context.Context) (string, error)) *versionLookup {
	return &versionLookup{ttl: ttl, probe: probe}
}

// get returns the cached worker version, refreshing the cache if
// expired. Always returns a string — empty when the worker is nil,
// unreachable, or slow.
func (vl *versionLookup) get(ctx context.Context) string {
	vl.mu.Lock()
	defer vl.mu.Unlock()

	if vl.probe == nil {
		return ""
	}
	if time.Now().Before(vl.expires) {
		return vl.value
	}

	probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()

	v, err := vl.probe(probeCtx)
	if err != nil {
		v = "" // unreachable, slow, or any other error → empty
	}
	vl.value = v
	vl.expires = time.Now().Add(vl.ttl)
	return v
}

// handleVersion serves GET /api/v1/version. The endpoint is public
// (mounted outside the auth group) and returns the canonical build
// metadata for the running API server, plus a best-effort worker
// version when reachable.
//
// The endpoint is intentionally public: version strings are not
// sensitive (commit sha is already exposed via /api/v1/admin/status,
// build date is innocuous, go runtime is fingerprintable from headers
// anyway). Tools like the web sidebar footer pull this on render.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	workerVer := ""
	if s.workerVersionLookup != nil {
		workerVer = s.workerVersionLookup.get(r.Context())
	}

	// Edition: cfg.Edition is the runtime source of truth (what the
	// operator deployed as). version.Edition is the build-time flavor
	// (informational, surfaced as buildEdition so misconfigurations
	// where the two disagree are visible).
	edition := s.cfg.Edition
	if edition == "" {
		edition = "unknown"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":       version.Version,
		"commit":        version.Commit,
		"buildDate":     version.BuildDate,
		"goVersion":     version.GoRuntime(),
		"edition":       edition,
		"buildEdition":  version.Edition,
		"workerVersion": workerVer,
	})
}
