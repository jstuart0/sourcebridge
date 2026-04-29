// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startSurrealContainer launches a SurrealDB container (in-memory mode) for
// integration tests. The container is torn down via t.Cleanup. Returns the
// connected *SurrealDB with all migrations applied, ready for test use.
func startSurrealContainer(t *testing.T) *SurrealDB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "surrealdb/surrealdb:v2.3.5",
		ExposedPorts: []string{"8000/tcp"},
		Cmd: []string{
			"start",
			"--user", "root",
			"--pass", "root",
			"memory",
		},
		// Wait for the log line AND for the TCP port to accept connections so
		// we don't race with colima's port-forwarding setup.
		WaitingFor: wait.ForAll(
			wait.ForLog("Started web server").WithStartupTimeout(30*time.Second),
			wait.ForListeningPort("8000/tcp").WithStartupTimeout(30*time.Second),
		),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start surreal container: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "8000")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}

	cfg := config.StorageConfig{
		SurrealMode:      "external",
		SurrealURL:       fmt.Sprintf("ws://%s:%s/rpc", host, port.Port()),
		SurrealUser:      "root",
		SurrealPass:      "root",
		SurrealNamespace: "test_ns",
		SurrealDatabase:  "test_db",
	}
	s := NewSurrealDB(cfg)

	connectCtx, connectCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer connectCancel()
	if err := s.Connect(connectCtx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Resolve migrations dir via runtime.Caller so the path is correct
	// regardless of the working directory from which the test is invoked.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "migrations")
	if _, statErr := os.Stat(migrationsDir); statErr != nil {
		t.Fatalf("migrations dir %s: %v", migrationsDir, statErr)
	}

	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer migrateCancel()
	if err := s.Migrate(migrateCtx, migrationsDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return s
}
