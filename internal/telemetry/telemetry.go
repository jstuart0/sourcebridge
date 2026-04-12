// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package telemetry provides anonymous, opt-out usage tracking.
//
// SourceBridge collects minimal, anonymous telemetry to understand how the
// product is used and prioritize improvements. No personally identifiable
// information is ever collected. Telemetry can be disabled via:
//
//   - Environment variable: SOURCEBRIDGE_TELEMETRY=off
//   - Config file:          [telemetry] enabled = false
//
// What is collected:
//   - A random installation ID (UUID, generated once, no PII)
//   - SourceBridge version and edition (oss/enterprise)
//   - Platform (OS and architecture)
//   - Aggregate counts (repositories indexed, reports generated)
//   - Active feature flags
//
// What is NOT collected:
//   - Repository names, URLs, or contents
//   - User names, emails, or credentials
//   - IP addresses (the server does not log them)
//   - Any source code or analysis results
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultEndpoint is the telemetry collection endpoint.
const DefaultEndpoint = "https://telemetry.sourcebridge.ai/v1/ping"

// Ping is the anonymous telemetry payload.
type Ping struct {
	InstallationID string            `json:"installation_id"`
	Version        string            `json:"version"`
	Edition        string            `json:"edition"`
	Platform       string            `json:"platform"`
	GoVersion      string            `json:"go_version"`
	Uptime         string            `json:"uptime"`
	Repos          int               `json:"repos"`
	Users          int               `json:"users"`
	Features       []string          `json:"features,omitempty"`
	Counts         map[string]int    `json:"counts,omitempty"`
	Timestamp      time.Time         `json:"timestamp"`
}

// CountProvider returns aggregate counts for telemetry.
type CountProvider interface {
	TelemetryCounts() (repos, users int, features []string, counts map[string]int)
}

// Tracker manages anonymous telemetry.
type Tracker struct {
	endpoint       string
	installationID string
	version        string
	edition        string
	dataDir        string
	startTime      time.Time
	enabled        bool
	interval       time.Duration
	provider       CountProvider
	client         *http.Client
	once           sync.Once
	stopCh         chan struct{}
}

// Option configures the tracker.
type Option func(*Tracker)

// WithEndpoint sets a custom telemetry endpoint.
func WithEndpoint(url string) Option {
	return func(t *Tracker) { t.endpoint = url }
}

// WithInterval sets the ping interval (default 24h).
func WithInterval(d time.Duration) Option {
	return func(t *Tracker) { t.interval = d }
}

// WithCountProvider sets the provider for aggregate counts.
func WithCountProvider(p CountProvider) Option {
	return func(t *Tracker) { t.provider = p }
}

// New creates a telemetry tracker. Call Start() to begin pinging.
func New(version, edition, dataDir string, opts ...Option) *Tracker {
	t := &Tracker{
		endpoint:  DefaultEndpoint,
		version:   version,
		edition:   edition,
		dataDir:   dataDir,
		startTime: time.Now(),
		enabled:   isEnabled(),
		interval:  24 * time.Hour,
		client:    &http.Client{Timeout: 10 * time.Second},
		stopCh:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(t)
	}
	t.installationID = t.loadOrCreateID()
	return t
}

// Start begins the telemetry loop. Non-blocking.
func (t *Tracker) Start() {
	if !t.enabled {
		slog.Info("telemetry disabled — no anonymous usage data will be sent",
			"opt_out", "set SOURCEBRIDGE_TELEMETRY=off or [telemetry] enabled = false")
		return
	}

	t.once.Do(func() {
		slog.Info("telemetry enabled — sending anonymous usage data",
			"endpoint", t.endpoint,
			"interval", t.interval.String(),
			"installation_id", t.installationID,
			"opt_out", "set SOURCEBRIDGE_TELEMETRY=off to disable",
		)
		go t.loop()
	})
}

// Stop terminates the telemetry loop.
func (t *Tracker) Stop() {
	if t.enabled {
		close(t.stopCh)
	}
}

// InstallationID returns the persistent installation UUID.
func (t *Tracker) InstallationID() string {
	return t.installationID
}

func (t *Tracker) loop() {
	// Send initial ping after a short delay (let the server finish starting)
	select {
	case <-time.After(30 * time.Second):
		t.send()
	case <-t.stopCh:
		return
	}

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.send()
		case <-t.stopCh:
			return
		}
	}
}

func (t *Tracker) send() {
	ping := Ping{
		InstallationID: t.installationID,
		Version:        t.version,
		Edition:        t.edition,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion:      runtime.Version(),
		Uptime:         time.Since(t.startTime).Truncate(time.Second).String(),
		Timestamp:      time.Now().UTC(),
	}

	if t.provider != nil {
		ping.Repos, ping.Users, ping.Features, ping.Counts = t.provider.TelemetryCounts()
	}

	body, err := json.Marshal(ping)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sourcebridge/"+t.version)

	resp, err := t.client.Do(req)
	if err != nil {
		// Silently fail — telemetry should never disrupt the application
		slog.Debug("telemetry ping failed", "error", err)
		return
	}
	defer resp.Body.Close()
	slog.Debug("telemetry ping sent", "status", resp.StatusCode)
}

func (t *Tracker) loadOrCreateID() string {
	if t.dataDir == "" {
		return uuid.New().String()
	}

	idFile := filepath.Join(t.dataDir, ".sourcebridge-installation-id")

	// Try to read existing ID
	data, err := os.ReadFile(idFile)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}

	// Generate new ID
	id := uuid.New().String()

	// Persist it (best-effort)
	if err := os.MkdirAll(t.dataDir, 0o755); err == nil {
		_ = os.WriteFile(idFile, []byte(id+"\n"), 0o644)
	}

	return id
}

// isEnabled checks environment variables and returns whether telemetry is on.
func isEnabled() bool {
	// Check SOURCEBRIDGE_TELEMETRY (primary opt-out)
	val := strings.ToLower(strings.TrimSpace(os.Getenv("SOURCEBRIDGE_TELEMETRY")))
	switch val {
	case "off", "false", "no", "0", "disabled":
		return false
	case "on", "true", "yes", "1", "enabled":
		return true
	}

	// Check DO_NOT_TRACK (community standard)
	if os.Getenv("DO_NOT_TRACK") == "1" {
		return false
	}

	// Default: enabled
	return true
}
