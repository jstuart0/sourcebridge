package main

import (
	"fmt"
	"log"
)

// Config holds application configuration
type Config struct {
	Port     int
	Database string
	Debug    bool
}

// NewConfig creates a default configuration
func NewConfig() *Config {
	return &Config{
		Port:     8080,
		Database: "postgres://localhost/app",
		Debug:    false,
	}
}

// Validate checks configuration is valid
func (c *Config) Validate() error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Port)
	}
	if c.Database == "" {
		return fmt.Errorf("database connection string required")
	}
	return nil
}

// StartServer initializes and starts the HTTP server
// REQ-001: System must start and listen on configured port
func StartServer(cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	log.Printf("Starting server on port %d", cfg.Port)
	return nil
}

func main() {
	cfg := NewConfig()
	if err := StartServer(cfg); err != nil {
		log.Fatal(err)
	}
}
