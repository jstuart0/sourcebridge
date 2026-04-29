package config

import (
	"os"
	"testing"
)

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
	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("expected anthropic provider, got %s", cfg.LLM.Provider)
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
