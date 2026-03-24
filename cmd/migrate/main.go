// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package main is the entry point for the database migration runner.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	surrealDB := db.NewSurrealDB(cfg.Storage)
	if err := surrealDB.Connect(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer surrealDB.Close()

	if err := surrealDB.Migrate(context.Background(), "internal/db/migrations"); err != nil {
		fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
		os.Exit(1)
	}

	slog.Info("migrations completed successfully")
}
