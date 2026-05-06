package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecurityDefaultsCSRFEnabled(t *testing.T) {
	cfg := Defaults()
	if !cfg.Security.CSRFEnabled {
		t.Error("Security.CSRFEnabled must default to true; found false — this is a security regression")
	}
}

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Server.HTTPPort != 8080 {
		t.Errorf("expected HTTP port 8080, got %d", cfg.Server.HTTPPort)
	}
	if cfg.Storage.SurrealMode != "embedded" {
		t.Errorf("expected embedded SurrealDB mode, got %s", cfg.Storage.SurrealMode)
	}
	if cfg.Security.Mode != "oss" {
		t.Errorf("expected oss security mode, got %s", cfg.Security.Mode)
	}
	// Provider is intentionally empty — fresh installs seed a blank Default
	// profile; the admin UI falls back to "ollama" for display.
	if cfg.LLM.Provider != "" {
		t.Errorf("expected empty LLM provider default (RC-1 fix), got %q", cfg.LLM.Provider)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
	}{
		{
			name:    "valid defaults",
			modify:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:    "invalid port",
			modify:  func(c *Config) { c.Server.HTTPPort = -1 },
			wantErr: true,
		},
		{
			name:    "invalid surreal mode",
			modify:  func(c *Config) { c.Storage.SurrealMode = "invalid" },
			wantErr: true,
		},
		{
			name:    "invalid redis mode",
			modify:  func(c *Config) { c.Storage.RedisMode = "invalid" },
			wantErr: true,
		},
		{
			name:    "invalid LLM provider",
			modify:  func(c *Config) { c.LLM.Provider = "unknown" },
			wantErr: true,
		},
		{
			name: "ollama without base_url",
			modify: func(c *Config) {
				c.LLM.Provider = "ollama"
				c.LLM.BaseURL = ""
			},
			wantErr: true,
		},
		{
			name: "ollama with base_url",
			modify: func(c *Config) {
				c.LLM.Provider = "ollama"
				c.LLM.BaseURL = "http://localhost:11434/v1"
			},
			wantErr: false,
		},
		{
			name: "vllm without base_url",
			modify: func(c *Config) {
				c.LLM.Provider = "vllm"
				c.LLM.BaseURL = ""
			},
			wantErr: true,
		},
		{
			name: "vllm with base_url",
			modify: func(c *Config) {
				c.LLM.Provider = "vllm"
				c.LLM.BaseURL = "http://localhost:8000/v1"
			},
			wantErr: false,
		},
		{
			name: "worker tls enabled without cert path",
			modify: func(c *Config) {
				c.Worker.TLS.Enabled = true
				c.Worker.TLS.KeyPath = "/etc/sourcebridge/tls/tls.key"
				c.Worker.TLS.CAPath = "/etc/sourcebridge/tls-ca/ca.crt"
			},
			wantErr: true,
		},
		{
			name: "worker tls enabled without key path",
			modify: func(c *Config) {
				c.Worker.TLS.Enabled = true
				c.Worker.TLS.CertPath = "/etc/sourcebridge/tls/tls.crt"
				c.Worker.TLS.CAPath = "/etc/sourcebridge/tls-ca/ca.crt"
			},
			wantErr: true,
		},
		{
			name: "worker tls enabled without ca path",
			modify: func(c *Config) {
				c.Worker.TLS.Enabled = true
				c.Worker.TLS.CertPath = "/etc/sourcebridge/tls/tls.crt"
				c.Worker.TLS.KeyPath = "/etc/sourcebridge/tls/tls.key"
			},
			wantErr: true,
		},
		{
			name: "worker tls enabled with all paths",
			modify: func(c *Config) {
				c.Worker.TLS.Enabled = true
				c.Worker.TLS.CertPath = "/etc/sourcebridge/tls/tls.crt"
				c.Worker.TLS.KeyPath = "/etc/sourcebridge/tls/tls.key"
				c.Worker.TLS.CAPath = "/etc/sourcebridge/tls-ca/ca.crt"
			},
			wantErr: false,
		},
		{
			name:    "worker tls disabled with empty paths",
			modify:  func(c *Config) {},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			tt.modify(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnvOverride(t *testing.T) {
	os.Setenv("SOURCEBRIDGE_SERVER_HTTP_PORT", "9090")
	defer os.Unsetenv("SOURCEBRIDGE_SERVER_HTTP_PORT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.HTTPPort != 9090 {
		t.Errorf("expected port 9090 from env, got %d", cfg.Server.HTTPPort)
	}
}

// TestDefaults_LLMProvider_IsEmpty asserts that the zero value for LLM.Provider
// is empty, not "anthropic". A future commit that re-introduces a non-empty
// default would cause fresh installs to seed a misleading Default profile.
// This test is a canary for that regression.
func TestDefaults_LLMProvider_IsEmpty(t *testing.T) {
	cfg := Defaults()
	if cfg.LLM.Provider != "" {
		t.Errorf("Defaults().LLM.Provider: want empty string (fresh-install-safe), got %q — "+
			"reverting to a non-empty provider default breaks fresh hub installs by seeding "+
			"a misconfigured Default profile. Set SOURCEBRIDGE_LLM_PROVIDER explicitly instead.", cfg.LLM.Provider)
	}
	if cfg.LLM.SummaryModel != "" {
		t.Errorf("Defaults().LLM.SummaryModel: want empty, got %q", cfg.LLM.SummaryModel)
	}
	if cfg.LLM.ReviewModel != "" {
		t.Errorf("Defaults().LLM.ReviewModel: want empty, got %q", cfg.LLM.ReviewModel)
	}
	if cfg.LLM.AskModel != "" {
		t.Errorf("Defaults().LLM.AskModel: want empty, got %q", cfg.LLM.AskModel)
	}
	// TimeoutSecs must stay non-zero — it's unrelated to provider choice.
	if cfg.LLM.TimeoutSecs == 0 {
		t.Errorf("Defaults().LLM.TimeoutSecs: want non-zero, got 0")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Phase 2 — ResolveEncryptionKey unit tests (r1 H2 resolution order, H4)
// ─────────────────────────────────────────────────────────────────────────

func TestResolveEncryptionKey_FileOnly(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "enc_key")
	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 hex chars = 32 bytes
	if err := os.WriteFile(keyFile, []byte(wantKey+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")

	sc := SecurityConfig{EncryptionKey: ""}
	key, source, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != wantKey {
		t.Errorf("key: got %q, want %q", key, wantKey)
	}
	if source != "file" {
		t.Errorf("source: got %q, want file", source)
	}
}

func TestResolveEncryptionKey_LiteralEnvOnly(t *testing.T) {
	os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")
	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sc := SecurityConfig{EncryptionKey: wantKey}
	key, source, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != wantKey {
		t.Errorf("key: got %q, want %q", key, wantKey)
	}
	if source != "literal-env" {
		t.Errorf("source: got %q, want literal-env", source)
	}
}

func TestResolveEncryptionKey_FileWinsOverEnv(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "enc_key")
	fileKey := "FILE-KEY-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := os.WriteFile(keyFile, []byte(fileKey), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")

	sc := SecurityConfig{EncryptionKey: "ENV-KEY-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}
	key, source, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != fileKey {
		t.Errorf("file should win: got %q, want %q", key, fileKey)
	}
	if source != "file" {
		t.Errorf("source: got %q, want file", source)
	}
}

func TestResolveEncryptionKey_FileMissingFallsBackToEnv(t *testing.T) {
	os.Setenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE", "/tmp/nonexistent-enc-key-xyz")
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")

	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sc := SecurityConfig{EncryptionKey: wantKey}
	key, source, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != wantKey {
		t.Errorf("key: got %q, want %q", key, wantKey)
	}
	if source != "file-missing-fallback-env" {
		t.Errorf("source: got %q, want file-missing-fallback-env", source)
	}
}

func TestResolveEncryptionKey_NeitherSet(t *testing.T) {
	os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")
	sc := SecurityConfig{EncryptionKey: ""}
	key, source, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != "" {
		t.Errorf("key: got %q, want empty", key)
	}
	if source != "unset" {
		t.Errorf("source: got %q, want unset", source)
	}
}

func TestResolveEncryptionKey_EmptyFileFallsToEnv(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "empty_key")
	if err := os.WriteFile(keyFile, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write empty key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")

	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sc := SecurityConfig{EncryptionKey: wantKey}
	key, source, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != wantKey {
		t.Errorf("key: got %q, want %q (empty file should fall through to env)", key, wantKey)
	}
	if source != "file-missing-fallback-env" {
		t.Errorf("source: got %q, want file-missing-fallback-env", source)
	}
}

func TestResolveEncryptionKey_FileTrailingNewlineTrimmed(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "enc_key")
	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(keyFile, []byte(wantKey+"\n\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")

	sc := SecurityConfig{}
	key, _, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != wantKey {
		t.Errorf("trailing newline not trimmed: got %q, want %q", key, wantKey)
	}
}

func TestResolveEncryptionKey_ShortKeyLogsError(t *testing.T) {
	// Short key (< 32 bytes) should still be returned — fail-soft.
	// We can't assert the slog.Error without capturing the logger,
	// but we verify the key is returned and no error is returned.
	os.Unsetenv("SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE")
	sc := SecurityConfig{EncryptionKey: "short"}
	key, source, err := sc.ResolveEncryptionKey()
	if err != nil {
		t.Fatalf("unexpected err (short key should be fail-soft): %v", err)
	}
	if key != "short" {
		t.Errorf("key: got %q, want short", key)
	}
	if source != "literal-env" {
		t.Errorf("source: got %q, want literal-env", source)
	}
}

func TestSecurityEnvBinding(t *testing.T) {
	cases := map[string]struct {
		env   string
		value string
		check func(*Config) string
	}{
		"encryption_key":        {"SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY", "test-passphrase-1", func(c *Config) string { return c.Security.EncryptionKey }},
		"jwt_secret":            {"SOURCEBRIDGE_SECURITY_JWT_SECRET", "test-jwt-2", func(c *Config) string { return c.Security.JWTSecret }},
		"github_webhook_secret": {"SOURCEBRIDGE_SECURITY_GITHUB_WEBHOOK_SECRET", "gh-3", func(c *Config) string { return c.Security.GitHubWebhookSecret }},
		"gitlab_webhook_secret": {"SOURCEBRIDGE_SECURITY_GITLAB_WEBHOOK_SECRET", "gl-4", func(c *Config) string { return c.Security.GitLabWebhookSecret }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			os.Setenv(tc.env, tc.value)
			defer os.Unsetenv(tc.env)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if got := tc.check(cfg); got != tc.value {
				t.Errorf("%s: expected %q from env %s, got %q", name, tc.value, tc.env, got)
			}
		})
	}
}
