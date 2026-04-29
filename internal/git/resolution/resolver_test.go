// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
)

// fakeStore implements GitConfigStore for tests. It serves the configured
// (token, ssh, version) triple and lets a test inject a deterministic
// loadErr / versionErr to drive failure paths.
type fakeStore struct {
	mu          sync.Mutex
	token       string
	sshKeyPath  string
	version     uint64
	loadErr     error
	versionErr  error
	loadCalls   int
	versionCalls int
	// blockVersion, when non-nil, blocks LoadGitConfigVersion until the
	// channel closes (used to test ctx cancellation).
	blockVersion chan struct{}
}

func (s *fakeStore) LoadGitConfig(ctx context.Context) (string, string, uint64, error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadCalls++
	if s.loadErr != nil {
		return "", "", s.version, s.loadErr
	}
	return s.token, s.sshKeyPath, s.version, nil
}

func (s *fakeStore) LoadGitConfigVersion(ctx context.Context) (uint64, error) {
	if s.blockVersion != nil {
		select {
		case <-s.blockVersion:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versionCalls++
	return s.version, s.versionErr
}

func TestResolve_BuiltinOnly_NoStoreNoEnv(t *testing.T) {
	r := New(nil, config.GitConfig{}, nil)
	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snap.Token != "" || snap.SSHKeyPath != "" {
		t.Fatalf("expected empty creds, got %+v", snap)
	}
	if snap.Sources[FieldToken] != SourceBuiltin {
		t.Fatalf("token source want builtin, got %q", snap.Sources[FieldToken])
	}
}

func TestResolve_EnvOnly_NoStore(t *testing.T) {
	r := New(nil, config.GitConfig{
		DefaultToken: "env-pat",
		SSHKeyPath:   "/etc/ssh/id_rsa",
	}, nil)
	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snap.Token != "env-pat" {
		t.Fatalf("token want env-pat, got %q", snap.Token)
	}
	if snap.SSHKeyPath != "/etc/ssh/id_rsa" {
		t.Fatalf("ssh path want /etc/ssh/id_rsa, got %q", snap.SSHKeyPath)
	}
	if snap.Sources[FieldToken] != SourceEnvFallback {
		t.Fatalf("token source want env_fallback, got %q", snap.Sources[FieldToken])
	}
}

func TestResolve_DBOverridesEnv(t *testing.T) {
	store := &fakeStore{
		token:      "db-pat",
		sshKeyPath: "/etc/sourcebridge/git-keys/id_ed25519",
		version:    7,
	}
	r := New(store, config.GitConfig{
		DefaultToken: "env-pat",
		SSHKeyPath:   "/etc/ssh/id_rsa",
	}, nil)
	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snap.Token != "db-pat" {
		t.Fatalf("token want db-pat, got %q", snap.Token)
	}
	if snap.SSHKeyPath != "/etc/sourcebridge/git-keys/id_ed25519" {
		t.Fatalf("ssh path want db value, got %q", snap.SSHKeyPath)
	}
	if snap.Sources[FieldToken] != SourceDB {
		t.Fatalf("token source want db, got %q", snap.Sources[FieldToken])
	}
	if snap.Version != 7 {
		t.Fatalf("version want 7, got %d", snap.Version)
	}
}

// TestResolve_DBClearsEnv verifies that an empty workspace value
// overrides the env-bootstrap default. Codex r2 high regression:
// the original implementation only overlaid non-empty DB values, so
// clearing the field in the UI silently let env win again. The
// resolver MUST treat an existing DB row as authoritative for every
// field, including empty.
func TestResolve_DBClearsEnv(t *testing.T) {
	store := &fakeStore{
		token:      "",  // operator cleared via UI
		sshKeyPath: "",  // operator cleared via UI
		version:    7,   // but a row EXISTS — the DB layer is authoritative
	}
	r := New(store, config.GitConfig{
		DefaultToken: "env-pat",
		SSHKeyPath:   "/etc/ssh/id_rsa",
	}, nil)
	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snap.Token != "" {
		t.Errorf("token: want empty (operator cleared via UI), got %q", snap.Token)
	}
	if snap.SSHKeyPath != "" {
		t.Errorf("ssh path: want empty (operator cleared via UI), got %q", snap.SSHKeyPath)
	}
	if snap.Sources[FieldToken] != SourceDB {
		t.Errorf("token source: want db, got %q", snap.Sources[FieldToken])
	}
	if snap.Sources[FieldSSHKeyPath] != SourceDB {
		t.Errorf("ssh source: want db, got %q", snap.Sources[FieldSSHKeyPath])
	}
}

func TestResolve_VersionKeyedCache(t *testing.T) {
	store := &fakeStore{
		token:   "db-pat",
		version: 1,
	}
	r := New(store, config.GitConfig{}, nil)

	// First Resolve: 1 version probe + 1 full load.
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("resolve 1: %v", err)
	}
	if store.loadCalls != 1 {
		t.Fatalf("first resolve: load want 1, got %d", store.loadCalls)
	}

	// Second Resolve at same version: 1 more version probe, NO new load.
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if store.loadCalls != 1 {
		t.Fatalf("second resolve at same version: load want 1, got %d", store.loadCalls)
	}

	// Bump version → next Resolve refetches.
	store.mu.Lock()
	store.version = 2
	store.token = "db-pat-v2"
	store.mu.Unlock()
	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve 3: %v", err)
	}
	if store.loadCalls != 2 {
		t.Fatalf("after bump: load want 2, got %d", store.loadCalls)
	}
	if snap.Token != "db-pat-v2" {
		t.Fatalf("token after bump want db-pat-v2, got %q", snap.Token)
	}
}

func TestResolve_DBOutage_CachedFallbackStale(t *testing.T) {
	store := &fakeStore{token: "db-pat", version: 1}
	r := New(store, config.GitConfig{DefaultToken: "env-pat"}, nil)

	// Prime the cache.
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Simulate a transient DB outage.
	store.mu.Lock()
	store.versionErr = errors.New("transient surreal connect failure")
	store.mu.Unlock()

	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve under outage: %v", err)
	}
	if snap.Token != "db-pat" {
		t.Fatalf("token under outage want cached db-pat, got %q", snap.Token)
	}
	if !snap.Stale {
		t.Fatalf("expected Stale=true under DB outage with cache")
	}
	if !snap.StaleFields[FieldToken] {
		t.Fatalf("expected StaleFields[token]=true")
	}
}

func TestResolve_DBOutage_NoCacheFallthroughToEnv(t *testing.T) {
	store := &fakeStore{
		token:      "db-pat",
		version:    1,
		versionErr: errors.New("connect refused"),
	}
	r := New(store, config.GitConfig{DefaultToken: "env-pat"}, nil)

	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// No cache → env-bootstrap layer wins.
	if snap.Token != "env-pat" {
		t.Fatalf("token want env-pat fallthrough, got %q", snap.Token)
	}
	if snap.Sources[FieldToken] != SourceEnvFallback {
		t.Fatalf("source want env_fallback, got %q", snap.Sources[FieldToken])
	}
}

func TestResolve_IntegrityFailure_FailsClosedNoEnvFallthrough(t *testing.T) {
	store := &fakeStore{
		token:   "(corrupt)",
		version: 1,
		loadErr: db.ErrGitTokenDecryptFailed,
	}
	r := New(store, config.GitConfig{
		DefaultToken: "env-pat-MUST-NOT-WIN",
		SSHKeyPath:   "/etc/ssh/id_env",
	}, nil)

	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snap.IntegrityError == nil {
		t.Fatalf("expected IntegrityError to be set on decrypt failure")
	}
	if snap.Token != "" {
		t.Fatalf("integrity-failed snapshot must have empty Token, got %q", snap.Token)
	}
	if snap.SSHKeyPath != "" {
		t.Fatalf("integrity-failed snapshot must have empty SSHKeyPath, got %q", snap.SSHKeyPath)
	}
	if snap.Sources[FieldToken] != SourceDB {
		t.Fatalf("source want db (the failing layer), got %q", snap.Sources[FieldToken])
	}
}

func TestResolve_InvalidateLocal(t *testing.T) {
	store := &fakeStore{token: "db-pat", version: 1}
	r := New(store, config.GitConfig{}, nil)

	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	if store.loadCalls != 1 {
		t.Fatalf("prime: load want 1, got %d", store.loadCalls)
	}

	// Cache hit (no new load).
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if store.loadCalls != 1 {
		t.Fatalf("cache hit: load want 1, got %d", store.loadCalls)
	}

	r.InvalidateLocal()

	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("resolve 3: %v", err)
	}
	if store.loadCalls != 2 {
		t.Fatalf("after invalidate: load want 2, got %d", store.loadCalls)
	}
}

func TestResolve_CtxCancelDuringVersionProbe(t *testing.T) {
	block := make(chan struct{})
	store := &fakeStore{token: "db-pat", version: 1, blockVersion: block}
	r := New(store, config.GitConfig{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before unblocking the version probe.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// resolver should observe ctx cancellation via the version probe.
	// We don't unblock; we let ctx.Done propagate.
	snap, err := r.Resolve(ctx)
	close(block)

	// The resolver swallows the version error and falls through to
	// builtin (no cache, no env). The error path returns nil so
	// the snapshot is empty + builtin-stamped, which is the safe
	// behavior under cancellation: we never serve a half-resolved
	// credential under a dying request. (Callers that want hard
	// cancellation propagation check ctx.Err() themselves before
	// using the snapshot.)
	if err != nil {
		t.Fatalf("resolve under cancel: unexpected err %v", err)
	}
	if snap.Token != "" {
		t.Fatalf("token under cancelled ctx with no env should be empty, got %q", snap.Token)
	}
}

func TestResolve_CtxAlreadyCancelled(t *testing.T) {
	store := &fakeStore{token: "db-pat", version: 1}
	r := New(store, config.GitConfig{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Resolve(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled when ctx already done, got %v", err)
	}
}

func TestResolve_NewRowEmptyVersionFallsThroughToEnv(t *testing.T) {
	// Fresh DB with no row yet → version=0, treated as no workspace
	// layer; env-bootstrap wins.
	store := &fakeStore{version: 0}
	r := New(store, config.GitConfig{DefaultToken: "env-pat"}, nil)

	snap, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snap.Token != "env-pat" {
		t.Fatalf("fresh-DB token want env-pat, got %q", snap.Token)
	}
	if snap.Sources[FieldToken] != SourceEnvFallback {
		t.Fatalf("fresh-DB source want env_fallback, got %q", snap.Sources[FieldToken])
	}
}
