// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package worker

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// TestClientBundleRotationSwapsAtomically verifies the bundle rotation
// path: starting a Client with insecure credentials, then triggering
// rotateConnection() (synthetically simulating a tlsreload.Watcher
// callback) results in a new bundle being installed without disrupting
// the test gRPC server. R3 slice 4.
func TestClientBundleRotationSwapsAtomically(t *testing.T) {
	srv, addr := startTestHealthServer(t)
	defer srv.Stop()

	c, err := New(addr, TLSConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	// Disable health check so the rotation doesn't depend on the test
	// server actually answering — we want to test the bundle swap
	// mechanics, not the health probe.
	c.healthCheckOnSwap = false

	original := c.bundle.Load()
	if original == nil {
		t.Fatal("expected initial bundle non-nil")
	}

	if err := c.rotateConnection(); err != nil {
		t.Fatalf("rotateConnection: %v", err)
	}

	rotated := c.bundle.Load()
	if rotated == original {
		t.Error("rotateConnection did not swap the bundle pointer")
	}
	if !original.closing.Load() {
		t.Error("expected the old bundle to be flagged closing after rotation")
	}
}

// TestClientRPCAcquiresBundleAndReleases verifies that a successful
// RPC increments and decrements the inflight counter on the active
// bundle. We use CheckHealth which goes through acquire/release.
func TestClientRPCAcquiresBundleAndReleases(t *testing.T) {
	srv, addr := startTestHealthServer(t)
	defer srv.Stop()

	c, err := New(addr, TLSConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	b := c.bundle.Load()

	if _, err := c.CheckHealth(context.Background()); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}

	if got := b.inflight.Load(); got != 0 {
		t.Errorf("inflight not released; got %d, want 0", got)
	}
}

// TestClientConcurrentRPCsDuringRotation simulates a tight rotation
// race: many goroutines run CheckHealth concurrently while the main
// goroutine triggers a rotation. No goroutine should panic or see
// errClientClosed; all RPCs should either complete on the old bundle
// or on the new one, with inflight always reaching zero on both.
//
// This is the test that catches acquire/release/closing races. Run
// with `-race` to surface any data race in the bundle-pointer dance.
func TestClientConcurrentRPCsDuringRotation(t *testing.T) {
	srv, addr := startTestHealthServer(t)
	defer srv.Stop()

	c, err := New(addr, TLSConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	c.healthCheckOnSwap = false

	const goroutines = 32
	const iterationsPerG = 5
	var wg sync.WaitGroup
	var rpcSuccess, rpcError atomic.Int64

	// Spawn RPC goroutines.
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterationsPerG; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, err := c.CheckHealth(ctx)
				cancel()
				if err != nil {
					rpcError.Add(1)
				} else {
					rpcSuccess.Add(1)
				}
			}
		}()
	}

	// Concurrently trigger rotations.
	for i := 0; i < 5; i++ {
		time.Sleep(2 * time.Millisecond)
		_ = c.rotateConnection()
	}

	wg.Wait()

	t.Logf("rpcs: success=%d error=%d", rpcSuccess.Load(), rpcError.Load())
	// We expect zero RPC failures: the test server is up the whole
	// time, and rotation should never produce a client-closed error.
	if rpcError.Load() != 0 {
		t.Errorf("expected zero RPC errors during rotation, got %d", rpcError.Load())
	}

	// After the dust settles, the latest bundle's inflight must be 0.
	final := c.bundle.Load()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && final.inflight.Load() != 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := final.inflight.Load(); got != 0 {
		t.Errorf("final bundle inflight: got %d, want 0", got)
	}
}

// TestClientCloseShortCircuitsRPCs verifies that calling Close() then
// invoking an RPC returns errClientClosed without panicking.
func TestClientCloseShortCircuitsRPCs(t *testing.T) {
	srv, addr := startTestHealthServer(t)
	defer srv.Stop()

	c, err := New(addr, TLSConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.CheckHealth(context.Background()); err == nil {
		t.Error("expected error after Close")
	}

	// Second Close is a no-op.
	if err := c.Close(); err != nil {
		t.Errorf("second Close should be no-op, got %v", err)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────

// healthServer is a minimal grpc health server that always reports
// SERVING. Sufficient for exercising acquire/release on a real
// connection.
type healthServer struct {
	healthpb.UnimplementedHealthServer
}

func (healthServer) Check(_ context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func startTestHealthServer(t *testing.T) (*grpc.Server, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	healthpb.RegisterHealthServer(srv, healthServer{})
	go func() {
		_ = srv.Serve(lis)
	}()
	return srv, lis.Addr().String()
}
