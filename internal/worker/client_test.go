// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package worker

import (
	"testing"
)

func TestNewClient(t *testing.T) {
	// New should succeed even with an unreachable address (lazy connect)
	c, err := New("localhost:59999")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer c.Close()

	if c.Address() != "localhost:59999" {
		t.Errorf("Address() = %q, want %q", c.Address(), "localhost:59999")
	}

	// Service clients should be non-nil
	if c.Reasoning == nil {
		t.Error("Reasoning client is nil")
	}
	if c.Linking == nil {
		t.Error("Linking client is nil")
	}
	if c.Requirements == nil {
		t.Error("Requirements client is nil")
	}
	if c.Health == nil {
		t.Error("Health client is nil")
	}
}

func TestIsAvailableNilClient(t *testing.T) {
	var c *Client
	if c.IsAvailable() {
		t.Error("nil client should not be available")
	}
}

func TestIsAvailableNotConnected(t *testing.T) {
	c, err := New("localhost:59999")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Without a real worker, should not be in READY state
	if c.IsAvailable() {
		t.Error("client without worker should not be available")
	}
}

func TestCloseNilClient(t *testing.T) {
	var c *Client
	err := c.Close()
	if err != nil {
		t.Errorf("Close() on nil client should return nil, got %v", err)
	}
}

func TestCloseClient(t *testing.T) {
	c, err := New("localhost:59999")
	if err != nil {
		t.Fatal(err)
	}

	err = c.Close()
	if err != nil {
		t.Errorf("Close() error: %v", err)
	}
}

func TestTimeoutConstants(t *testing.T) {
	// Verify timeouts are sensible
	if TimeoutHealth <= 0 {
		t.Error("TimeoutHealth should be positive")
	}
	if TimeoutAnalysis <= TimeoutHealth {
		t.Error("TimeoutAnalysis should be greater than TimeoutHealth")
	}
	if TimeoutReview < TimeoutAnalysis {
		t.Error("TimeoutReview should be >= TimeoutAnalysis")
	}
	if TimeoutLinkTotal <= TimeoutLinkItem {
		t.Error("TimeoutLinkTotal should be greater than TimeoutLinkItem")
	}
}
