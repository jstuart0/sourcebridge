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
//   - Platform (OS and architecture; can be overridden via
//     SOURCEBRIDGE_TELEMETRY_PLATFORM=test for homelab/dev/CI installs)
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

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// DefaultEndpoint is the telemetry collection endpoint.
const DefaultEndpoint = "https://telemetry.sourcebridge.ai/v1/ping"

// Ping is the anonymous telemetry payload.
type Ping struct {
	InstallationID  string         `json:"installation_id"`
	Version         string         `json:"version"`
	Edition         string         `json:"edition"`
	LLMProviderKind string         `json:"llm_provider_kind,omitempty"`
	Platform        string         `json:"platform"`
	GoVersion       string         `json:"go_version"`
	Uptime          string         `json:"uptime"`
	Repos           int            `json:"repos"`
	Users           int            `json:"users"`
	Features        []string       `json:"features,omitempty"`
	Counts          map[string]int `json:"counts,omitempty"`
	Timestamp       time.Time      `json:"timestamp"`

	// Clustering fields (Sprint 1 — F5 Subsystem Clustering).

	// ClusteringEnabled is true when the subsystem_clustering capability is
	// active on this installation.
	ClusteringEnabled bool `json:"clustering_enabled"`
	// ClusterCount is the number of clusters for the largest indexed repository.
	// Zero when clustering has not run or no repos are indexed.
	ClusterCount int `json:"cluster_count"`
	// ClusteringModularityQ is the Newman–Girvan modularity score of the most
	// recent clustering run, rounded to 2 decimal places. Zero when clustering
	// has not run.
	ClusteringModularityQ float32 `json:"clustering_modularity_q"`
	// AgentSetupUsed is true when `sourcebridge setup claude` has been run at
	// least once on this installation. Reserved for Sprint 3 — always false
	// in v1.
	AgentSetupUsed bool `json:"agent_setup_used"`
}

// CountProvider returns aggregate counts for telemetry.
type CountProvider interface {
	TelemetryCounts() (repos, users int, features []string, counts map[string]int)
}

// Tracker manages anonymous telemetry.
type Tracker struct {
	endpoint        string
	installationID  string
	version         string
	edition         string
	dataDir         string
	startTime       time.Time
	enabled         bool
	interval        time.Duration
	llmProviderKind string
	provider        CountProvider
	client          *http.Client
	once            sync.Once
	stopCh          chan struct{}
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

// WithLLMProviderKind sets the configured provider class reported in telemetry.
// Expected values are "local" or "cloud".
func WithLLMProviderKind(kind string) Option {
	return func(t *Tracker) { t.llmProviderKind = strings.TrimSpace(strings.ToLower(kind)) }
}

// WithConfig wires the config-backed telemetry settings into the tracker so
// that isEnabled() uses the shared GloballyEnabled policy (Decision 5).
// When not set, the tracker falls back to the legacy env-only path.
func WithConfig(cfg config.TelemetryConfig) Option {
	return func(t *Tracker) { t.enabled = GloballyEnabled(cfg) }
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

// agentSetupFlagFile returns the path of the persistent flag file written by
// `sourcebridge setup claude` upon its first successful run.
func agentSetupFlagFile(dataDir string) string {
	return filepath.Join(dataDir, ".sourcebridge-agent-setup-used")
}

// MarkAgentSetupUsed writes the persistent flag file to the data directory so
// that subsequent telemetry pings report agent_setup_used=true. Exported for
// use by the CLI after a successful `setup claude` run.
func MarkAgentSetupUsed(dataDir string) {
	if dataDir == "" {
		return
	}
	path := agentSetupFlagFile(dataDir)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("1\n"), 0o644)
}

func (t *Tracker) send() {
	ping := Ping{
		InstallationID:  t.installationID,
		Version:         t.version,
		Edition:         t.edition,
		LLMProviderKind: t.llmProviderKind,
		Platform:        resolvePlatform(),
		GoVersion:       runtime.Version(),
		Uptime:          time.Since(t.startTime).Truncate(time.Second).String(),
		Timestamp:       time.Now().UTC(),
	}

	if t.provider != nil {
		ping.Repos, ping.Users, ping.Features, ping.Counts = t.provider.TelemetryCounts()
	}

	// agent_setup_used: set from the persistent flag file written by `setup claude`.
	if t.dataDir != "" {
		if _, err := os.Stat(agentSetupFlagFile(t.dataDir)); err == nil {
			ping.AgentSetupUsed = true
		}
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

// resolvePlatform returns the platform string sent to the telemetry
// collector. Defaults to "<goos>/<goarch>" so production installs
// report their real platform. The SOURCEBRIDGE_TELEMETRY_PLATFORM env
// var overrides the default — used by homelab/dev/CI deploys to set
// it to "test" so the collector's auto-flag rule
// (`platform == "test"`) excludes those installs from public counts.
//
// Setting the override to anything else is permitted but discouraged.
// Empty / unset → real platform.
func resolvePlatform() string {
	if v := strings.TrimSpace(os.Getenv("SOURCEBRIDGE_TELEMETRY_PLATFORM")); v != "" {
		return v
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

// isEnabled is the legacy env-only telemetry gate, used when New() is called
// without WithConfig. It delegates to GloballyEnabled with a synthetic
// TelemetryConfig that reflects the raw SOURCEBRIDGE_TELEMETRY env token.
//
// Behavior change vs. the pre-Phase-2 implementation: env "on" no longer
// forces telemetry true when the config file has disabled it. In this
// env-only path the config Enabled field is always true (no file was
// supplied), so env "on" is a no-op in practice — but the key contract is
// that calling-code using WithConfig can no longer be re-enabled by env
// (Decision 5). This function stays for back-compat with call sites that
// don't yet pass WithConfig; production callers in cli/serve.go should
// use WithConfig(cfg.Telemetry) so the full chain is honoured.
func isEnabled() bool {
	cfg := config.TelemetryConfig{
		Enabled:       true, // default; WithConfig overrides this with the real file value
		FunnelEnabled: true,
	}
	raw := os.Getenv("SOURCEBRIDGE_TELEMETRY")
	if raw != "" {
		cfg.EnvOverride = parseTelemetryEnvTokenLegacy(raw)
	}
	return GloballyEnabled(cfg)
}

// parseTelemetryEnvTokenLegacy converts the SOURCEBRIDGE_TELEMETRY string to a
// *bool using the same token set as config.parseTelemetryEnvToken but
// without importing the config package (avoiding a cycle). Returns nil on
// ambiguous/empty input.
func parseTelemetryEnvTokenLegacy(s string) *bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "off", "no", "false", "0", "disabled":
		f := false
		return &f
	case "on", "yes", "true", "1", "enabled":
		t := true
		return &t
	}
	return nil
}
