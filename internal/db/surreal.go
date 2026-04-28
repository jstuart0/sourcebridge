// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// SurrealDB wraps the SurrealDB client with automatic reconnection.
type SurrealDB struct {
	cfg       config.StorageConfig
	db        *surrealdb.DB
	connected bool
	mu        sync.RWMutex
	stopKeep  chan struct{}
}

// NewSurrealDB creates a SurrealDB client based on configuration.
func NewSurrealDB(cfg config.StorageConfig) *SurrealDB {
	return &SurrealDB{cfg: cfg}
}

// Connect establishes the database connection.
func (s *SurrealDB) Connect(ctx context.Context) error {
	if s.cfg.SurrealMode == "embedded" {
		slog.Info("using in-memory store (embedded mode)", "path", s.cfg.SurrealDataPath)
		s.connected = true
		return nil
	}

	slog.Info("connecting to external SurrealDB", "url", s.cfg.SurrealURL)
	return s.dial(ctx)
}

// dial creates a new websocket connection, authenticates, and selects the namespace/database.
// Caller must NOT hold s.mu.
func (s *SurrealDB) dial(ctx context.Context) error {
	db, err := surrealdb.New(s.cfg.SurrealURL)
	if err != nil {
		return fmt.Errorf("surrealdb connect: %w", err)
	}

	if _, err := db.SignIn(ctx, surrealdb.Auth{
		Username: s.cfg.SurrealUser,
		Password: s.cfg.SurrealPass,
	}); err != nil {
		db.Close(ctx)
		return fmt.Errorf("surrealdb signin: %w", err)
	}

	if err := db.Use(ctx, s.cfg.SurrealNamespace, s.cfg.SurrealDatabase); err != nil {
		db.Close(ctx)
		return fmt.Errorf("surrealdb use ns/db: %w", err)
	}

	s.mu.Lock()
	s.db = db
	s.connected = true
	s.mu.Unlock()

	slog.Info("connected to SurrealDB",
		"namespace", s.cfg.SurrealNamespace,
		"database", s.cfg.SurrealDatabase,
	)
	return nil
}

// reconnect closes the stale connection and establishes a fresh one.
func (s *SurrealDB) reconnect(ctx context.Context) error {
	s.mu.Lock()
	// Double-check: if another goroutine already reconnected, verify the connection works.
	if s.db != nil {
		testCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := surrealdb.Query[interface{}](testCtx, s.db, "RETURN true", nil)
		cancel()
		if err == nil {
			s.mu.Unlock()
			return nil // Connection is fine — another goroutine already fixed it.
		}
	}

	// Tear down stale connection.
	if s.db != nil {
		s.db.Close(context.Background())
		s.db = nil
	}
	s.connected = false
	s.mu.Unlock()

	slog.Info("surrealdb reconnecting", "url", s.cfg.SurrealURL)
	return s.dial(ctx)
}

// StartKeepalive launches a background goroutine that pings SurrealDB
// every 15 seconds and reconnects automatically on failure.
func (s *SurrealDB) StartKeepalive() {
	if s.cfg.SurrealMode == "embedded" {
		return
	}
	s.stopKeep = make(chan struct{})
	go s.keepaliveLoop()
	slog.Info("surrealdb keepalive started", "interval", "15s")
}

// StopKeepalive stops the background keepalive goroutine.
func (s *SurrealDB) StopKeepalive() {
	if s.stopKeep != nil {
		close(s.stopKeep)
	}
}

func (s *SurrealDB) keepaliveLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.ping()
		case <-s.stopKeep:
			return
		}
	}
}

// Ping executes a trivial round-trip ("RETURN true") against SurrealDB and
// returns nil when the DB is reachable. It satisfies the rest.DBPinger
// interface so the /readyz handler and serviceHealth GraphQL resolver can
// share a single health-check path without importing internal/db directly.
func (s *SurrealDB) Ping(ctx context.Context) error {
	db := s.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := surrealdb.Query[interface{}](ctx, db, "RETURN true", nil)
	if err != nil {
		return fmt.Errorf("surrealdb ping: %w", err)
	}
	return nil
}

func (s *SurrealDB) ping() {
	db := s.DB()
	if db == nil {
		// Connection is down (previous reconnect failed) — keep trying.
		slog.Info("surrealdb connection is down, attempting reconnect")
		reconCtx, reconCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer reconCancel()
		if reconErr := s.reconnect(reconCtx); reconErr != nil {
			slog.Error("surrealdb reconnect failed", "error", reconErr)
		} else {
			slog.Info("surrealdb reconnected successfully")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := surrealdb.Query[interface{}](ctx, db, "RETURN true", nil)
	if err != nil {
		slog.Warn("surrealdb keepalive ping failed, reconnecting", "error", err)
		reconCtx, reconCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer reconCancel()
		if reconErr := s.reconnect(reconCtx); reconErr != nil {
			slog.Error("surrealdb reconnect failed", "error", reconErr)
		} else {
			slog.Info("surrealdb reconnected successfully")
		}
	}
}

// Close stops keepalive and closes the database connection.
func (s *SurrealDB) Close() error {
	s.StopKeepalive()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close(context.Background())
		s.db = nil
	}
	s.connected = false
	return nil
}

// IsConnected returns whether the database is connected.
func (s *SurrealDB) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected
}

// DB returns the underlying surrealdb.DB handle for use with the generic
// surrealdb.Query and other SDK functions.  Returns nil in embedded mode.
func (s *SurrealDB) DB() *surrealdb.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

// isConnectionError returns true if the error indicates a broken websocket.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "write: connection reset by peer") ||
		strings.Contains(msg, "context deadline exceeded")
}

// Query executes a SurrealQL query with automatic reconnection on connection errors.
func (s *SurrealDB) Query(ctx context.Context, sql string, params map[string]interface{}) (interface{}, error) {
	db := s.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	results, err := surrealdb.Query[interface{}](ctx, db, sql, params)
	if err != nil {
		if isConnectionError(err) {
			slog.Warn("connection error in query, attempting reconnect", "error", err)
			if reconErr := s.reconnect(ctx); reconErr != nil {
				return nil, fmt.Errorf("surrealdb query: %w (reconnect also failed: %v)", err, reconErr)
			}
			db = s.DB()
			if db == nil {
				return nil, fmt.Errorf("database not connected after reconnect")
			}
			results, err = surrealdb.Query[interface{}](ctx, db, sql, params)
			if err != nil {
				return nil, fmt.Errorf("surrealdb query after reconnect: %w", err)
			}
			return results, nil
		}
		return nil, fmt.Errorf("surrealdb query: %w", err)
	}

	return results, nil
}

// Migrate reads .surql files from migrationsDir and executes them in
// lexicographic order.  Already-applied migrations (tracked via the
// schema_version table) are skipped.
func (s *SurrealDB) Migrate(ctx context.Context, migrationsDir string) error {
	if !s.connected || s.db == nil {
		return fmt.Errorf("database not connected")
	}
	slog.Info("running migrations", "dir", migrationsDir)

	// Discover migration files
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".surql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	if len(files) == 0 {
		slog.Info("no migration files found")
		return nil
	}

	// Determine already-applied version
	appliedVersion := 0
	versionResults, err := surrealdb.Query[[]map[string]interface{}](ctx, s.db,
		"SELECT version FROM schema_version ORDER BY version DESC LIMIT 1", nil)
	if err == nil && versionResults != nil && len(*versionResults) > 0 {
		qr := (*versionResults)[0]
		if qr.Error == nil {
			rows := qr.Result
			if len(rows) > 0 {
				if v, ok := rows[0]["version"]; ok {
					switch vt := v.(type) {
					case float64:
						appliedVersion = int(vt)
					case int:
						appliedVersion = vt
					case uint64:
						appliedVersion = int(vt)
					}
				}
			}
		}
	}
	slog.Info("current schema version", "version", appliedVersion)

	// Execute each migration file in order, skipping any whose
	// version is already applied. Migration filenames follow
	// "NNN_description.surql" (three-digit leading version); the
	// version field on schema_version is bumped after each
	// successful apply so subsequent boots don't re-run destructive
	// migrations (REMOVE TABLE, etc.).
	for _, fname := range files {
		version := migrationVersion(fname)
		if version == 0 {
			slog.Warn("migration filename missing numeric prefix; skipping", "file", fname)
			continue
		}
		if version <= appliedVersion {
			slog.Debug("skipping already-applied migration", "file", fname, "version", version)
			continue
		}
		fpath := filepath.Join(migrationsDir, fname)
		data, err := os.ReadFile(fpath)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", fname, err)
		}

		sql := string(data)

		slog.Info("applying migration", "file", fname, "version", version)
		if _, err := surrealdb.Query[interface{}](ctx, s.db, sql, nil); err != nil {
			return fmt.Errorf("executing migration %s: %w", fname, err)
		}

		// Record the applied version so later boots skip it.
		if _, err := surrealdb.Query[interface{}](ctx, s.db,
			"CREATE schema_version SET version = $v, applied_at = time::now()",
			map[string]any{"v": version}); err != nil {
			// If the row creation fails, subsequent boots will re-run
			// the migration. That's the conservative failure mode —
			// re-running an idempotent migration is safe, dropping a
			// user's data is not.
			slog.Warn("schema_version upsert failed; migration may re-run next boot",
				"file", fname, "version", version, "error", err)
		}
		appliedVersion = version
		slog.Info("migration applied", "file", fname, "version", version)
	}

	return nil
}

// migrationVersion extracts the numeric prefix from a migration
// filename — "021_compliance_assessment.surql" → 21. Returns 0 when
// the filename doesn't begin with digits.
func migrationVersion(fname string) int {
	n := 0
	for _, c := range fname {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
