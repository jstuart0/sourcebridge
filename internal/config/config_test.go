package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexingDefaultsAllowPrivateGitHostsFalse pins the default value of
// AllowPrivateGitHosts to false. This is a security regression canary:
// a commit that changes the default to true would allow operators' API pods
// to reach RFC1918 hosts without explicit opt-in, enabling SSRF.
func TestIndexingDefaultsAllowPrivateGitHostsFalse(t *testing.T) {
	cfg := Defaults()
	if cfg.Indexing.AllowPrivateGitHosts {
		t.Error("Indexing.AllowPrivateGitHosts must default to false; found true — " +
			"this is a security regression that enables SSRF on git clone. " +
			"Only set SOURCEBRIDGE_INDEXING_ALLOW_PRIVATE_GIT_HOSTS=true on " +
			"single-operator self-hosted instances. See CA-312.")
	}
}

func TestSecurityDefaultsCSRFEnabled(t *testing.T) {
	cfg := Defaults()
	if !cfg.Security.CSRFEnabled {
		t.Error("Security.CSRFEnabled must default to true; found false — this is a security regression")
	}
}

func TestSecurityDefaultsCSRFFullCoverageEnabled(t *testing.T) {
	cfg := Defaults()
	if cfg.Security.CSRFFullCoverageEnabled {
		t.Error("Security.CSRFFullCoverageEnabled must default to false; found true — would activate the broader CSRF gate (admin route group + Bearer-bypass tightening + auth-helper-route gating) on deploy without operator opt-in. Phase-1 frontend coverage must be confirmed live before this flag is flipped on per the rollout runbook in docs/admin/llm-config.md.")
	}
}

// TestLLMDefaultsAllowPrivateBaseURLTrue pins the default value of
// AllowPrivateBaseURL to true. This is intentional: local LLM providers
// (Ollama on localhost, vLLM on internal cluster IPs) require private-IP
// access. Multi-tenant SaaS operators who must block SSRF via crafted base
// URLs should flip this to false per docs/going-to-production.md (CA-214).
func TestLLMDefaultsAllowPrivateBaseURLTrue(t *testing.T) {
	cfg := Defaults()
	if !cfg.LLM.AllowPrivateBaseURL {
		t.Error("LLM.AllowPrivateBaseURL must default to true; found false — " +
			"local providers (Ollama, vLLM, llama-cpp, etc.) require localhost/private-IP access. " +
			"Set SOURCEBRIDGE_LLM_ALLOW_PRIVATE_BASE_URL=false only on multi-tenant public deployments. " +
			"See CA-214.")
	}
}

func TestMCPDefaultsPublicProbeTrue(t *testing.T) {
	cfg := Defaults()
	if !cfg.MCP.PublicProbe {
		t.Error("MCP.PublicProbe must default to true; found false — " +
			"this would hide the HEAD /api/v1/mcp/http probe from unauthenticated " +
			"callers by default, breaking the web UI MCP onboarding banner. " +
			"Set SOURCEBRIDGE_MCP_PUBLIC_PROBE=false explicitly to opt in to hiding the probe. " +
			"See CA-314.")
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
			// CA-311: Validate() now enforces a ≥32-byte JWT secret. The
			// happy-path Validate() tests don't go through Load() (which
			// auto-resolves/auto-generates the secret), so we seed a
			// well-formed placeholder here. Tests that specifically
			// exercise the JWT-secret length gate set their own value.
			if cfg.Security.JWTSecret == "" {
				cfg.Security.JWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			}
			tt.modify(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRejectsShortJWTSecret(t *testing.T) {
	cfg := Defaults()
	cfg.Security.JWTSecret = "short"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() should reject JWT secret shorter than 32 bytes")
	}
}

func TestValidateAcceptsExactly32ByteJWTSecret(t *testing.T) {
	cfg := Defaults()
	cfg.Security.JWTSecret = "01234567890123456789012345678901" // 32 ASCII bytes
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected 32-byte JWT secret: %v", err)
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

// ─────────────────────────────────────────────────────────────────────────
// CA-311 — ResolveJWTSecret unit tests (mirror ResolveEncryptionKey)
// ─────────────────────────────────────────────────────────────────────────

func TestResolveJWTSecret_FileOnly(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "jwt_secret")
	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(keyFile, []byte(wantKey+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")

	sc := SecurityConfig{JWTSecret: ""}
	key, source, err := sc.ResolveJWTSecret()
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

func TestResolveJWTSecret_LiteralEnvOnly(t *testing.T) {
	os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")
	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sc := SecurityConfig{JWTSecret: wantKey}
	key, source, err := sc.ResolveJWTSecret()
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

func TestResolveJWTSecret_FileWinsOverEnv(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "jwt_secret")
	fileKey := "FILE-JWT-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := os.WriteFile(keyFile, []byte(fileKey), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")

	sc := SecurityConfig{JWTSecret: "ENV-JWT-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}
	key, source, err := sc.ResolveJWTSecret()
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

func TestResolveJWTSecret_FileMissingFallsBackToEnv(t *testing.T) {
	os.Setenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE", "/tmp/nonexistent-jwt-secret-xyz")
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")

	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sc := SecurityConfig{JWTSecret: wantKey}
	key, source, err := sc.ResolveJWTSecret()
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

// codex r2 High: explicit _FILE that's unreadable + no literal-env fallback
// must FAIL CLOSED, not silently auto-generate. Otherwise a typoed path or
// missing Secret mount creates per-process JWT keys = replica split-brain.
func TestResolveJWTSecret_FileMissingNoFallback_ReturnsError(t *testing.T) {
	os.Setenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE", "/tmp/nonexistent-jwt-secret-fail-closed")
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")

	sc := SecurityConfig{JWTSecret: ""}
	_, _, err := sc.ResolveJWTSecret()
	if err == nil {
		t.Fatal("ResolveJWTSecret() should return error when _FILE is set + unreadable + no literal fallback")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("error should mention unreadable: %v", err)
	}
}

// codex r2 High companion: empty file + no literal-env fallback also fails.
func TestResolveJWTSecret_EmptyFileNoFallback_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "empty_jwt_secret_no_fallback")
	if err := os.WriteFile(keyFile, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write empty key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")

	sc := SecurityConfig{JWTSecret: ""}
	_, _, err := sc.ResolveJWTSecret()
	if err == nil {
		t.Fatal("ResolveJWTSecret() should return error when _FILE points to empty file + no literal fallback")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty: %v", err)
	}
}

func TestResolveJWTSecret_NeitherSet(t *testing.T) {
	os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")
	sc := SecurityConfig{JWTSecret: ""}
	key, source, err := sc.ResolveJWTSecret()
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

func TestResolveJWTSecret_EmptyFileFallsToEnv(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "empty_jwt_secret")
	if err := os.WriteFile(keyFile, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write empty key file: %v", err)
	}
	os.Setenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE", keyFile)
	defer os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")

	wantKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sc := SecurityConfig{JWTSecret: wantKey}
	key, source, err := sc.ResolveJWTSecret()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if key != wantKey {
		t.Errorf("key: got %q, want %q", key, wantKey)
	}
	if source != "file-missing-fallback-env" {
		t.Errorf("source: got %q, want file-missing-fallback-env (empty file treated as missing)", source)
	}
}

func TestLoadAutoGeneratesJWTSecretWhenUnset(t *testing.T) {
	// Unset all JWT-secret env vars so Load() takes the auto-generate path.
	os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET")
	os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := len(cfg.Security.JWTSecret); got != 64 {
		t.Errorf("auto-generated JWT secret length: got %d, want 64 (32 raw bytes hex-encoded)", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("auto-generated secret should pass Validate(): %v", err)
	}

	// A second Load() should produce a different secret (non-deterministic
	// confirms it's actually random; not a fixture).
	cfg2, err := Load()
	if err != nil {
		t.Fatalf("Load() #2 error: %v", err)
	}
	if cfg.Security.JWTSecret == cfg2.Security.JWTSecret {
		t.Error("two Load() calls produced identical auto-generated secrets — non-determinism check failed")
	}
}

func TestSecurityJWTSecretFileViperBinding(t *testing.T) {
	// Ensure SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE binds to
	// SecurityConfig.JWTSecretFile via Viper (not just os.Getenv).
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "jwt_secret")
	wantKey := "viperjwt-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(keyFile, []byte(wantKey+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	// Use the field directly (config.toml path) — env-var path is covered
	// by the other ResolveJWTSecret tests.
	os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET")
	os.Unsetenv("SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE")
	sc := SecurityConfig{JWTSecretFile: keyFile}
	key, source, err := sc.ResolveJWTSecret()
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

// TestServerDefaultsRejectWildcardCORSWithCredentials pins the default to false.
// Changing this default to true would cause Validate() to fail on any deployment
// that has a wildcard CORS origin — including the default "http://localhost:3300".
func TestServerDefaultsRejectWildcardCORSWithCredentials(t *testing.T) {
	cfg := Defaults()
	if cfg.Server.RejectWildcardCORSWithCredentials {
		t.Error("Server.RejectWildcardCORSWithCredentials must default to false; " +
			"changing to true would break any install with a wildcard cors_origin on upgrade. " +
			"Operators opt in via SOURCEBRIDGE_SERVER_REJECT_WILDCARD_CORS_WITH_CREDENTIALS=true. " +
			"See CA-313.")
	}
}

// TestCORSWildcardGuard_FlagOff verifies that wildcard origins pass Validate() when the
// strict rejection flag is off (default). The WARN log fires but validation succeeds.
func TestCORSWildcardGuard_FlagOff(t *testing.T) {
	wildcards := []string{
		"*",
		"*.example.com",
		"https://*.bar.com",
		" *.foo.com", // whitespace-padded
	}
	for _, origin := range wildcards {
		cfg := validCORSTestConfig()
		cfg.Server.CORSOrigins = []string{origin}
		cfg.Server.RejectWildcardCORSWithCredentials = false
		if err := cfg.Validate(); err != nil {
			t.Errorf("origin %q: expected no error with flag=false, got %v", origin, err)
		}
	}
}

// TestCORSWildcardGuard_FlagOn verifies that wildcard origins fail Validate() when the
// strict rejection flag is on, and that non-wildcard origins still pass.
func TestCORSWildcardGuard_FlagOn(t *testing.T) {
	wildcards := []string{
		"*",
		"*.example.com",
		"https://*.bar.com",
		" *.foo.com", // whitespace-padded
	}
	for _, origin := range wildcards {
		cfg := validCORSTestConfig()
		cfg.Server.CORSOrigins = []string{origin}
		cfg.Server.RejectWildcardCORSWithCredentials = true
		err := cfg.Validate()
		if err == nil {
			t.Errorf("origin %q: expected error with flag=true, got nil", origin)
			continue
		}
		if !strings.Contains(err.Error(), "wildcard pattern") {
			t.Errorf("origin %q: expected 'wildcard pattern' in error, got: %v", origin, err)
		}
	}

	// Non-wildcard origins must still pass when flag is on.
	safe := []string{"https://app.example.com", "http://localhost:3300", "https://sub.example.com"}
	for _, origin := range safe {
		cfg := validCORSTestConfig()
		cfg.Server.CORSOrigins = []string{origin}
		cfg.Server.RejectWildcardCORSWithCredentials = true
		if err := cfg.Validate(); err != nil {
			t.Errorf("safe origin %q: unexpected error with flag=true: %v", origin, err)
		}
	}
}

// validCORSTestConfig returns a minimal valid Config for CORS guard tests.
// It avoids the Defaults() JWT auto-generate path by supplying a placeholder secret.
func validCORSTestConfig() *Config {
	cfg := Defaults()
	// Defaults() does not populate JWTSecret; Validate() requires ≥32 bytes.
	cfg.Security.JWTSecret = strings.Repeat("a", 64)
	return cfg
}
