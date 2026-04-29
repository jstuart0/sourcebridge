// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package resolution provides the runtime git-credential resolver that
// every clone / fetch / upstream-probe call flows through. The resolver
// is the single source of truth for the default PAT and SSH key path;
// it replaces the legacy boot-time merge in cli/serve.go that allowed
// k8s configmap env vars to silently override DB-saved admin settings —
// the same bug shape the LLM workstream fixed for ca_llm_config.
//
// Resolution order (per Resolve call):
//
//  1. Workspace settings (ca_git_config), version-keyed cache so a save
//     on replica A is visible to replica B on the very next Resolve.
//  2. Env-var bootstrap (cfg.Git, populated at boot from
//     SOURCEBRIDGE_GIT_DEFAULT_TOKEN / SOURCEBRIDGE_GIT_SSH_KEY_PATH).
//  3. Built-in defaults (empty).
//
// Fail-closed: if the workspace row exists, has the v1 envelope prefix,
// but cannot be decrypted (corruption, key rotation, missing key), the
// resolver returns the snapshot with Snapshot.IntegrityError set and an
// empty Token. It does NOT fall through to the env-bootstrap layer.
// Callers MUST check IntegrityError and abort the operation rather than
// silently downgrade to the env value — fail-closed is the whole point
// of the envelope.
//
// The resolver mirrors internal/llm/resolution exactly: same version-cell
// pattern, same Stale flag for transient DB outages, same InvalidateLocal
// for post-save cache nudges. There is no TTL — version-cell read on
// every Resolve is the dedupe primitive.
package resolution

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
)

// Source labels which layer supplied a given field's final value.
type Source string

const (
	SourceBuiltin     Source = "builtin"
	SourceEnvFallback Source = "env_fallback"
	SourceDB          Source = "db"
)

const (
	FieldToken      = "token"
	FieldSSHKeyPath = "ssh_key_path"
)

// Snapshot is the fully-resolved git credential view for a single
// Resolve call.
//
// IMPORTANT: never log Snapshot.Token. The structured log helper in this
// package (LogResolved) emits token_set:bool only.
type Snapshot struct {
	Token      string
	SSHKeyPath string

	// Sources maps field name → the layer that supplied that field's
	// final value.
	Sources map[string]Source

	// Version is the workspace DB record's version stamp at resolve
	// time. Zero when the snapshot's token did not come from the DB
	// (env-only or builtin path).
	Version uint64

	// Stale is true when the resolver could not reach the DB on this
	// Resolve and returned a cached Snapshot instead. The Token transport
	// is unchanged; admins may want to surface the staleness to the UI.
	Stale bool

	// StaleFields, when Stale=true, marks per-field which values came
	// from the now-unreachable workspace layer.
	StaleFields map[string]bool

	// IntegrityError, when non-nil, signals a hard fail-closed condition:
	// the workspace row exists with the v1 envelope but cannot be
	// decrypted (corruption, key rotation without re-save, missing
	// SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY in the env). When set:
	//   - Token and SSHKeyPath are empty.
	//   - The resolver did NOT fall through to the env-bootstrap layer.
	//   - Callers MUST check this and abort the operation, rather than
	//     silently downgrading to the env value.
	//
	// Distinct from a transient DB outage: when the DB is unreachable
	// (not a decrypt failure), Stale is true and IntegrityError is nil
	// — the cached snapshot or env-bootstrap fallback is acceptable.
	IntegrityError error
}

// GitConfigStore is the narrow interface the resolver needs. The full
// persistence struct is db.SurrealGitConfigStore; we keep the resolver's
// view minimal so tests can fake it without dragging in SurrealDB.
type GitConfigStore interface {
	LoadGitConfig(ctx context.Context) (token, sshKeyPath string, version uint64, err error)
	LoadGitConfigVersion(ctx context.Context) (uint64, error)
}

// Resolver returns a Snapshot for a credential lookup. The default
// implementation is *DefaultResolver; tests substitute their own.
type Resolver interface {
	Resolve(ctx context.Context) (Snapshot, error)
	// InvalidateLocal nudges the local cache to drop its workspace
	// snapshot so the next Resolve refetches even before the version
	// stamp drifts. Called by the REST PUT handler after a save on
	// this replica. Cross-replica freshness still relies on the
	// version-cell read in Resolve.
	InvalidateLocal()
}

// DefaultResolver implements Resolver against a workspace store and an
// env-bootstrap snapshot taken from cfg.Git.
type DefaultResolver struct {
	store   GitConfigStore
	envBoot config.GitConfig
	log     *slog.Logger

	mu       sync.Mutex
	cache    *cachedRecord
	cacheVer uint64
}

type cachedRecord struct {
	token      string
	sshKeyPath string
}

// New constructs a DefaultResolver. envBoot is captured by value so
// subsequent mutations on the caller's *config.Config do not affect
// resolution. Callers MUST NOT mutate cfg.Git after constructing the
// resolver.
//
// store may be nil in embedded mode; in that case Resolve always falls
// through to the env-bootstrap layer.
func New(store GitConfigStore, envBoot config.GitConfig, log *slog.Logger) *DefaultResolver {
	if log == nil {
		log = slog.Default()
	}
	return &DefaultResolver{
		store:   store,
		envBoot: envBoot,
		log:     log,
	}
}

// Resolve produces the Snapshot. When the DB returns ErrGitTokenDecryptFailed
// the snapshot's IntegrityError is set and the env-bootstrap layer is
// NOT applied; callers MUST surface this rather than fall through.
func (r *DefaultResolver) Resolve(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{
		Sources: make(map[string]Source, 2),
	}

	// Layer 3 (lowest priority): builtin (empty).
	applyBuiltin(&snap)

	// Layer 2: env-bootstrap (cfg.Git from boot).
	applyEnvBoot(&snap, r.envBoot)

	// Layer 1: workspace DB. May set IntegrityError, in which case we
	// roll back the env-bootstrap layer to fail closed.
	r.applyWorkspace(ctx, &snap)

	if snap.IntegrityError != nil {
		// Fail-closed: clear any env values we layered in step 2.
		snap.Token = ""
		snap.SSHKeyPath = ""
		// Mark the affected fields as DB-sourced so a downstream log
		// shows the layer that produced the error, not the env layer.
		snap.Sources[FieldToken] = SourceDB
		snap.Sources[FieldSSHKeyPath] = SourceDB
	}

	return snap, nil
}

// InvalidateLocal drops the cached workspace snapshot. Called by the
// REST PUT handler after a save on the same replica so the next Resolve
// fetches fresh values without waiting for version-stamp drift.
func (r *DefaultResolver) InvalidateLocal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = nil
	r.cacheVer = 0
}

func applyBuiltin(snap *Snapshot) {
	snap.Sources[FieldToken] = SourceBuiltin
	snap.Sources[FieldSSHKeyPath] = SourceBuiltin
}

func applyEnvBoot(snap *Snapshot, env config.GitConfig) {
	if env.DefaultToken != "" {
		snap.Token = env.DefaultToken
		snap.Sources[FieldToken] = SourceEnvFallback
	}
	if env.SSHKeyPath != "" {
		snap.SSHKeyPath = env.SSHKeyPath
		snap.Sources[FieldSSHKeyPath] = SourceEnvFallback
	}
}

// applyWorkspace fetches the workspace record (cache-aware) and overlays
// any non-empty fields onto snap.
//
// On a transient DB outage we fall back to the previously cached snapshot
// and stamp Stale=true, leaving env-bootstrap values in place where the
// cache is empty.
//
// On an integrity failure (decrypt error on a prefixed value) we set
// snap.IntegrityError so the caller fails closed.
func (r *DefaultResolver) applyWorkspace(ctx context.Context, snap *Snapshot) {
	if r.store == nil {
		return
	}

	// Cheap version probe first.
	currentVer, verErr := r.store.LoadGitConfigVersion(ctx)

	r.mu.Lock()
	cachedRec := r.cache
	cachedVer := r.cacheVer
	r.mu.Unlock()

	if verErr != nil {
		// DB outage: serve cached snapshot if we have one.
		if cachedRec != nil {
			r.log.Warn("git resolver: workspace version probe failed; serving cached snapshot",
				"error", verErr,
				"cached_version", cachedVer)
			r.overlayFromCache(snap, cachedRec, cachedVer, true /*stale*/)
			snap.Stale = true
		} else {
			r.log.Warn("git resolver: workspace unreachable and no cached snapshot; falling through to env",
				"error", verErr)
		}
		return
	}

	// No row yet → workspace layer is empty.
	if currentVer == 0 {
		return
	}

	// Cache hit: reuse the cached record without a re-fetch.
	if cachedRec != nil && cachedVer == currentVer {
		r.overlayFromCache(snap, cachedRec, cachedVer, false)
		return
	}

	// Cache miss: full fetch.
	tok, ssh, ver, loadErr := r.store.LoadGitConfig(ctx)
	if loadErr != nil {
		// Distinguish integrity failure from transient outage.
		if errors.Is(loadErr, db.ErrGitTokenDecryptFailed) {
			r.log.Error("git resolver: workspace token decrypt failed; failing closed",
				"error", loadErr,
				"version", ver)
			snap.IntegrityError = loadErr
			return
		}
		// Transient: serve cache + Stale=true.
		if cachedRec != nil {
			r.log.Warn("git resolver: workspace fetch failed; serving cached snapshot",
				"error", loadErr)
			r.overlayFromCache(snap, cachedRec, cachedVer, true)
			snap.Stale = true
		} else {
			r.log.Warn("git resolver: workspace fetch failed; falling through to env",
				"error", loadErr)
		}
		return
	}

	// Successful fetch: update cache + overlay.
	rec := &cachedRecord{token: tok, sshKeyPath: ssh}
	r.mu.Lock()
	r.cache = rec
	r.cacheVer = ver
	r.mu.Unlock()

	r.overlayFromCache(snap, rec, ver, false)
}

// overlayFromCache applies a cached record onto snap with SourceDB
// stamping. When stale=true also records per-field stale markers.
func (r *DefaultResolver) overlayFromCache(snap *Snapshot, rec *cachedRecord, ver uint64, stale bool) {
	mark := func(field string) {
		snap.Sources[field] = SourceDB
		if stale {
			if snap.StaleFields == nil {
				snap.StaleFields = make(map[string]bool)
			}
			snap.StaleFields[field] = true
		}
	}
	if rec.token != "" {
		snap.Token = rec.token
		mark(FieldToken)
	}
	if rec.sshKeyPath != "" {
		snap.SSHKeyPath = rec.sshKeyPath
		mark(FieldSSHKeyPath)
	}
	snap.Version = ver
}

// LogResolved emits the per-call structured log line operators grep for
// to confirm the workspace settings are taking effect (e.g.
// sources_token == "db" instead of "env_fallback"). NEVER logs the raw
// token — only token_set:bool.
//
// Callers (the clone/fetch consumers) invoke this once per top-level git
// op. The log format is stable; downstream tooling parses the
// sources_<field> values and the integrity_error_set flag.
func LogResolved(log *slog.Logger, op string, snap Snapshot) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("git creds resolved",
		"op", op,
		"token_set", snap.Token != "",
		"ssh_key_path_set", snap.SSHKeyPath != "",
		"version", snap.Version,
		"stale", snap.Stale,
		"integrity_error_set", snap.IntegrityError != nil,
		"sources_token", snap.Sources[FieldToken],
		"sources_ssh_key_path", snap.Sources[FieldSSHKeyPath],
	)
}
