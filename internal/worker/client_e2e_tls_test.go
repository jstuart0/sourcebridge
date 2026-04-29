// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package worker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"

	"github.com/sourcebridge/sourcebridge/internal/worker/tlsreload"
)

// TestE2E_RotationCommitsNewCertAfterPassingProbe is the end-to-end
// proof that the cert pipeline (watcher → candidate → probe → commit
// → bundle swap → fresh-handshake-uses-new-cert) wires through
// correctly against a REAL mTLS gRPC server.
//
// R3 followups B3. Codex r1+r1b on the followups delivery plan
// requested a real-server test rather than the unit-level
// rotation-mechanics tests we already have.
func TestE2E_RotationCommitsNewCertAfterPassingProbe(t *testing.T) {
	dir := t.TempDir()
	caCertPath, caKeyPath := generateTestCA(t, dir)
	const serverName = "worker.test.local"
	serverCertPath, serverKeyPath := generateServerCertSignedBy(t, dir, "worker-server", caCertPath, caKeyPath, serverName)
	clientCertPath, clientKeyPath := generateClientCertSignedBy(t, dir, "client-v1", caCertPath, caKeyPath)

	// Spin up a real mTLS gRPC server. The server records the SerialNumber
	// of the client cert it sees on each unary RPC.
	srv, addr, peerSerials := startMTLSHealthServer(t, serverCertPath, serverKeyPath, caCertPath)
	defer srv.Stop()

	// Build a tlsreload.Watcher pointed at the client cert files.
	w, err := tlsreload.New(tlsreload.Config{
		CertPath:          clientCertPath,
		KeyPath:           clientKeyPath,
		CAPath:            caCertPath,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("tlsreload.New: %v", err)
	}
	defer w.Close()

	// Build the worker.Client with the watcher wired.
	c, err := New(addr, TLSConfig{
		Enabled:    true,
		CertPath:   clientCertPath,
		KeyPath:    clientKeyPath,
		CAPath:     caCertPath,
		ServerName: serverName,
	}, WithTLSReloadWatcher(w))
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	defer c.Close()

	// Test-only synchronization: subscribe to rotation completion.
	rotateDone := make(chan tlsreload.Candidate, 4)
	c.onRotateCompleteHook(func(cand tlsreload.Candidate) {
		rotateDone <- cand
	})

	// Step 1: a real RPC over the live mTLS connection.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.CheckHealth(ctx); err != nil {
		t.Fatalf("initial CheckHealth over mTLS: %v", err)
	}

	v1Cert := readCertSerial(t, clientCertPath)
	if !peerSerials.contains(v1Cert) {
		t.Fatalf("server should have seen client-v1 serial %s; saw %v",
			v1Cert.String(), peerSerials.snapshot())
	}

	// Step 2: rotate the client cert files atomically (write next-to,
	// then rename — exact pattern Kubernetes Secret-volume swap uses).
	v2Path, v2Key := generateClientCertSignedBy(t, dir, "client-v2", caCertPath, caKeyPath)
	v2Serial := readCertSerial(t, v2Path)
	if err := os.Rename(v2Path, clientCertPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(v2Key, clientKeyPath); err != nil {
		t.Fatal(err)
	}

	// Trigger watcher reload (in production fsnotify drives this; in
	// tests we call Reload directly to keep the test deterministic).
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Wait for rotation completion via the test hook.
	select {
	case cand := <-rotateDone:
		if cand.Generation == 0 {
			t.Fatal("expected rotation candidate generation > 0")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rotation never completed (probe failed?)")
	}

	// Step 3: a fresh RPC. Server should now see v2's serial.
	peerSerials.clear()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := c.CheckHealth(ctx2); err != nil {
		t.Fatalf("post-rotation CheckHealth: %v", err)
	}
	if !peerSerials.contains(v2Serial) {
		t.Errorf("expected server to see v2 serial %s after rotation; saw %v",
			v2Serial.String(), peerSerials.snapshot())
	}
	if peerSerials.contains(v1Cert) {
		t.Errorf("server saw v1 serial %s in a post-rotation RPC; new bundle should have replaced it",
			v1Cert.String())
	}
}

// TestE2E_FailedProbe_PreservesActiveBundle exercises the codex r2b
// scenario the B2 invariant defends against: a candidate is staged,
// the probe fails, and a subsequent RPC on the active bundle still
// presents the previous cert. We synthesize the probe failure by
// rotating to a cert whose CA the server doesn't trust.
func TestE2E_FailedProbe_PreservesActiveBundle(t *testing.T) {
	dir := t.TempDir()
	caCertPath, caKeyPath := generateTestCA(t, dir)
	const serverName = "worker.test.local"
	serverCertPath, serverKeyPath := generateServerCertSignedBy(t, dir, "worker-server", caCertPath, caKeyPath, serverName)
	clientCertPath, clientKeyPath := generateClientCertSignedBy(t, dir, "client-v1", caCertPath, caKeyPath)

	srv, addr, peerSerials := startMTLSHealthServer(t, serverCertPath, serverKeyPath, caCertPath)
	defer srv.Stop()

	// Watcher with chain-verification ON — so the bogus-CA-signed
	// candidate fails Stage validation up front.
	w, err := tlsreload.New(tlsreload.Config{
		CertPath:          clientCertPath,
		KeyPath:           clientKeyPath,
		CAPath:            caCertPath,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("tlsreload.New: %v", err)
	}
	defer w.Close()

	c, err := New(addr, TLSConfig{
		Enabled:    true,
		CertPath:   clientCertPath,
		KeyPath:    clientKeyPath,
		CAPath:     caCertPath,
		ServerName: serverName,
	}, WithTLSReloadWatcher(w))
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	defer c.Close()

	// Confirm the live mTLS path works and lock in the v1 serial.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.CheckHealth(ctx); err != nil {
		t.Fatalf("initial CheckHealth: %v", err)
	}
	v1Serial := readCertSerial(t, clientCertPath)
	committedBefore := w.CommittedGeneration()

	// Rotate to a client cert signed by a DIFFERENT CA. Stage will
	// reject this because chain verification fails — Stage returns
	// an error; no candidate is produced; OnCandidate never fires.
	bogusDir := filepath.Join(dir, "bogus")
	if err := os.MkdirAll(bogusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bogusCAPath, bogusCAKeyPath := generateTestCA(t, bogusDir)
	bogusCert, bogusKey := generateClientCertSignedBy(t, dir, "client-bogus", bogusCAPath, bogusCAKeyPath)
	if err := os.Rename(bogusCert, clientCertPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bogusKey, clientKeyPath); err != nil {
		t.Fatal(err)
	}

	// Reload SHOULD return an error (Stage validation fails).
	reloadErr := w.Reload()
	if reloadErr == nil {
		t.Fatal("expected Reload to error when cert is signed by an unknown CA")
	}

	// CRITICAL: the active bundle's RPCs still present v1.
	if w.CommittedGeneration() != committedBefore {
		t.Errorf("failed Stage advanced CommittedGeneration: %d -> %d",
			committedBefore, w.CommittedGeneration())
	}
	peerSerials.clear()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := c.CheckHealth(ctx2); err != nil {
		t.Fatalf("post-failed-rotation CheckHealth: %v", err)
	}
	if !peerSerials.contains(v1Serial) {
		t.Errorf("expected server to still see v1 serial %s; saw %v",
			v1Serial.String(), peerSerials.snapshot())
	}
}

// TestE2E_PostProbeFailure_FiresDroppedHookAndPreservesBundle exercises
// the path codex r2 Medium 2 specifically called out: Stage SUCCEEDS
// (cert is well-formed and chains to a CA the watcher trusts) but the
// post-probe FAILS (the mTLS server rejects the new client cert because
// it's signed by a CA the SERVER doesn't trust).
//
// We achieve this by starting the server with a ClientCAs pool that
// trusts only `caA`, while the watcher trusts both `caA` and `caB`.
// The watcher initial cert is signed by caA (works). The rotation
// candidate is signed by caB (Stage passes — caB chains to one of
// the watcher's trusted roots; probe fails — server rejects caB).
func TestE2E_PostProbeFailure_FiresDroppedHookAndPreservesBundle(t *testing.T) {
	dir := t.TempDir()
	caAPath, caAKeyPath := generateTestCA(t, dir)
	bogusDir := filepath.Join(dir, "bogus")
	if err := os.MkdirAll(bogusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	caBPath, caBKeyPath := generateTestCA(t, bogusDir)

	// Watcher's CA bundle = caA + caB combined. Stage validates
	// against this combined pool, so a cert signed by EITHER CA
	// passes Stage validation.
	combinedCAPath := filepath.Join(dir, "combined-ca.crt")
	caABytes, err := os.ReadFile(caAPath)
	if err != nil {
		t.Fatal(err)
	}
	caBBytes, err := os.ReadFile(caBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(combinedCAPath, append(caABytes, caBBytes...), 0o644); err != nil {
		t.Fatal(err)
	}

	const serverName = "worker.test.local"
	serverCertPath, serverKeyPath := generateServerCertSignedBy(t, dir, "worker-server", caAPath, caAKeyPath, serverName)
	clientCertPath, clientKeyPath := generateClientCertSignedBy(t, dir, "client-v1", caAPath, caAKeyPath)

	// Server's ClientCAs pool ONLY includes caA — server will REJECT
	// any client cert signed by caB.
	srv, addr, peerSerials := startMTLSHealthServer(t, serverCertPath, serverKeyPath, caAPath)
	defer srv.Stop()

	// Watcher uses the combined CA pool for Stage validation. Disable
	// chain verification because the watcher's helper doesn't know
	// how to "succeed against either CA in a multi-CA bundle" cleanly;
	// for this test we just want Stage to produce a valid Candidate
	// with the bogus-CA cert.
	w, err := tlsreload.New(tlsreload.Config{
		CertPath:          clientCertPath,
		KeyPath:           clientKeyPath,
		CAPath:            combinedCAPath,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("tlsreload.New: %v", err)
	}
	defer w.Close()

	c, err := New(addr, TLSConfig{
		Enabled:    true,
		CertPath:   clientCertPath,
		KeyPath:    clientKeyPath,
		CAPath:     combinedCAPath,
		ServerName: serverName,
	}, WithTLSReloadWatcher(w))
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	defer c.Close()

	droppedCh := make(chan struct {
		cand   tlsreload.Candidate
		reason error
	}, 4)
	c.onCandidateDroppedHook(func(cand tlsreload.Candidate, reason error) {
		droppedCh <- struct {
			cand   tlsreload.Candidate
			reason error
		}{cand, reason}
	})
	rotateDone := make(chan tlsreload.Candidate, 4)
	c.onRotateCompleteHook(func(cand tlsreload.Candidate) {
		rotateDone <- cand
	})

	// Initial RPC works.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.CheckHealth(ctx); err != nil {
		t.Fatalf("initial CheckHealth: %v", err)
	}
	v1Serial := readCertSerial(t, clientCertPath)
	if !peerSerials.contains(v1Serial) {
		t.Fatalf("server should see v1 serial; saw %v", peerSerials.snapshot())
	}
	committedBefore := w.CommittedGeneration()

	// Rotate to a cert signed by caB. Stage passes (cert chains to
	// caB which IS in the combined CA pool). Probe fails (server
	// only trusts caA).
	v2Cert, v2Key := generateClientCertSignedBy(t, dir, "client-v2-bogus", caBPath, caBKeyPath)
	if err := os.Rename(v2Cert, clientCertPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(v2Key, clientKeyPath); err != nil {
		t.Fatal(err)
	}

	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v (expected Stage to pass)", err)
	}

	// Wait on the dropped hook — proves Stage produced a candidate
	// AND the rotation rejected it post-probe.
	select {
	case d := <-droppedCh:
		if d.cand.Generation == 0 {
			t.Error("dropped candidate Generation should be > 0")
		}
		if d.reason == nil {
			t.Error("dropped candidate reason should be non-nil")
		}
	case <-rotateDone:
		t.Fatal("rotation completed but should have failed the probe")
	case <-time.After(5 * time.Second):
		t.Fatal("OnCandidateDropped never fired")
	}

	// Watcher's committed generation is unchanged.
	if w.CommittedGeneration() != committedBefore {
		t.Errorf("failed-probe rotation advanced CommittedGeneration: %d -> %d",
			committedBefore, w.CommittedGeneration())
	}

	// A fresh RPC still presents v1's serial. Need to remember the
	// client cert files were rotated to v2 on disk; the active
	// bundle should still hold v1 in its immutable snapshot.
	peerSerials.clear()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := c.CheckHealth(ctx2); err != nil {
		t.Fatalf("post-failed-probe CheckHealth: %v", err)
	}
	if !peerSerials.contains(v1Serial) {
		t.Errorf("expected server to still see v1 serial %s; saw %v",
			v1Serial.String(), peerSerials.snapshot())
	}
}

// TestE2E_StaleCommit_FiresDroppedHook covers codex r2 Medium 2's
// other half: when a candidate's Commit returns ok=false (a newer
// candidate already won), the dropped hook now fires. Without this
// fix, observability tools watching the dropped channel would miss
// half the "rotation didn't change anything" cases.
func TestE2E_StaleCommit_FiresDroppedHook(t *testing.T) {
	dir := t.TempDir()
	caCertPath, caKeyPath := generateTestCA(t, dir)
	const serverName = "worker.test.local"
	serverCertPath, serverKeyPath := generateServerCertSignedBy(t, dir, "worker-server", caCertPath, caKeyPath, serverName)
	clientCertPath, clientKeyPath := generateClientCertSignedBy(t, dir, "client-v1", caCertPath, caKeyPath)

	srv, addr, _ := startMTLSHealthServer(t, serverCertPath, serverKeyPath, caCertPath)
	defer srv.Stop()

	w, err := tlsreload.New(tlsreload.Config{
		CertPath:          clientCertPath,
		KeyPath:           clientKeyPath,
		CAPath:            caCertPath,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("tlsreload.New: %v", err)
	}
	defer w.Close()

	c, err := New(addr, TLSConfig{
		Enabled:    true,
		CertPath:   clientCertPath,
		KeyPath:    clientKeyPath,
		CAPath:     caCertPath,
		ServerName: serverName,
	}, WithTLSReloadWatcher(w))
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	defer c.Close()

	droppedCh := make(chan tlsreload.Candidate, 4)
	c.onCandidateDroppedHook(func(cand tlsreload.Candidate, _ error) {
		droppedCh <- cand
	})

	// Stage v2 and v3, with v3 winning the Commit race.
	v2Cert, v2Key := generateClientCertSignedBy(t, dir, "client-v2", caCertPath, caKeyPath)
	if err := os.Rename(v2Cert, clientCertPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(v2Key, clientKeyPath); err != nil {
		t.Fatal(err)
	}
	cand2, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage v2: %v", err)
	}

	v3Cert, v3Key := generateClientCertSignedBy(t, dir, "client-v3", caCertPath, caKeyPath)
	if err := os.Rename(v3Cert, clientCertPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(v3Key, clientKeyPath); err != nil {
		t.Fatal(err)
	}
	cand3, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage v3: %v", err)
	}

	// Commit v3 directly (skipping the rotation pipeline so we can
	// control timing). Now v2 will lose at Commit time.
	ok, err := w.Commit(cand3)
	if err != nil || !ok {
		t.Fatalf("Commit v3: ok=%v err=%v", ok, err)
	}

	// Synthesize the v2 rotation pipeline: rotateForCandidate(cand2)
	// will Stage-pass (it's already staged), probe-pass (server is
	// up), then Commit-fail (v3 already won). Dropped hook should
	// fire.
	rotErr := c.rotateForCandidate(cand2)
	if rotErr != nil {
		t.Fatalf("rotateForCandidate v2: %v", rotErr)
	}

	select {
	case dropped := <-droppedCh:
		if dropped.Generation != cand2.Generation {
			t.Errorf("dropped Generation: got %d, want %d",
				dropped.Generation, cand2.Generation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnCandidateDropped never fired for stale Commit")
	}
}

// TestE2E_HeldOpenRPCDrainsAcrossRotation covers codex r2 Medium 2's
// "long-running RPC held open across rotation" gap: a CheckHealth
// against a delaying server is launched, rotation fires while it's
// in flight, the call completes successfully, and we observe that
// the active bundle on call return is the NEW one (rotation
// happened) but the call itself ran on the OLD bundle's conn.
func TestE2E_HeldOpenRPCDrainsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	caCertPath, caKeyPath := generateTestCA(t, dir)
	const serverName = "worker.test.local"
	serverCertPath, serverKeyPath := generateServerCertSignedBy(t, dir, "worker-server", caCertPath, caKeyPath, serverName)
	clientCertPath, clientKeyPath := generateClientCertSignedBy(t, dir, "client-v1", caCertPath, caKeyPath)

	// Start a slow mTLS health server that waits 1 second per Check.
	srv, addr, peerSerials := startSlowMTLSHealthServer(t, serverCertPath, serverKeyPath, caCertPath, 1*time.Second)
	defer srv.Stop()

	w, err := tlsreload.New(tlsreload.Config{
		CertPath:          clientCertPath,
		KeyPath:           clientKeyPath,
		CAPath:            caCertPath,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("tlsreload.New: %v", err)
	}
	defer w.Close()

	c, err := New(addr, TLSConfig{
		Enabled:    true,
		CertPath:   clientCertPath,
		KeyPath:    clientKeyPath,
		CAPath:     caCertPath,
		ServerName: serverName,
	}, WithTLSReloadWatcher(w))
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	defer c.Close()

	rotateDone := make(chan tlsreload.Candidate, 4)
	c.onRotateCompleteHook(func(cand tlsreload.Candidate) {
		rotateDone <- cand
	})

	// Snapshot the initial bundle pointer so we can assert it
	// changed after rotation.
	initialBundle := c.bundle.Load()
	if initialBundle == nil {
		t.Fatal("initial bundle should be non-nil")
	}
	v1Serial := readCertSerial(t, clientCertPath)

	// Launch a slow CheckHealth in the background. The slow server
	// guarantees this stays in-flight while we trigger rotation.
	rpcDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.CheckHealth(ctx)
		rpcDone <- err
	}()

	// Give the RPC a moment to land on the old bundle before we
	// rotate.
	time.Sleep(100 * time.Millisecond)
	if got := initialBundle.inflight.Load(); got < 1 {
		t.Errorf("expected initial bundle inflight >= 1 after launching slow RPC; got %d", got)
	}

	// Trigger rotation while the RPC is in flight.
	v2Cert, v2Key := generateClientCertSignedBy(t, dir, "client-v2", caCertPath, caKeyPath)
	if err := os.Rename(v2Cert, clientCertPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(v2Key, clientKeyPath); err != nil {
		t.Fatal(err)
	}
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Wait for rotation completion.
	select {
	case <-rotateDone:
	case <-time.After(3 * time.Second):
		t.Fatal("rotation never completed")
	}

	// New bundle should be different from the initial one.
	if c.bundle.Load() == initialBundle {
		t.Error("rotation completed but bundle pointer did not change")
	}

	// The old bundle should be flagged closing but its inflight
	// is still > 0 (the slow RPC is still running).
	if !initialBundle.closing.Load() {
		t.Error("old bundle should be flagged closing after rotation")
	}

	// The slow RPC must complete successfully — i.e. drain works.
	select {
	case err := <-rpcDone:
		if err != nil {
			t.Errorf("held-open RPC should have completed; got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("held-open RPC never returned")
	}

	// The server saw the v1 serial during the held-open call (the
	// call ran on the old bundle).
	if !peerSerials.contains(v1Serial) {
		t.Errorf("server should have seen v1 serial during held-open call; saw %v",
			peerSerials.snapshot())
	}

	// After the slow RPC returns, the old bundle's drain goroutine
	// closes the conn. Give it a beat then assert inflight is 0.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if initialBundle.inflight.Load() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := initialBundle.inflight.Load(); got != 0 {
		t.Errorf("old bundle inflight after RPC completion: got %d, want 0", got)
	}
}

// ─── E2E test helpers ─────────────────────────────────────────────

type serialSet struct {
	mu   sync.Mutex
	seen []*big.Int
}

func newSerialSet() *serialSet { return &serialSet{} }

func (s *serialSet) add(serial *big.Int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, new(big.Int).Set(serial))
}

func (s *serialSet) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = nil
}

func (s *serialSet) contains(serial *big.Int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.seen {
		if x.Cmp(serial) == 0 {
			return true
		}
	}
	return false
}

func (s *serialSet) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.seen))
	for i, x := range s.seen {
		out[i] = x.String()
	}
	return out
}

// peerCapturingHealthServer extends the basic healthServer to capture
// the SerialNumber of the client cert it sees on each Check call.
type peerCapturingHealthServer struct {
	healthpb.UnimplementedHealthServer
	serials *serialSet
	count   atomic.Int64
}

func (h *peerCapturingHealthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	h.count.Add(1)
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			if len(tlsInfo.State.PeerCertificates) > 0 {
				h.serials.add(tlsInfo.State.PeerCertificates[0].SerialNumber)
			}
		}
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

// slowPeerCapturingHealthServer is identical to peerCapturingHealthServer
// except every Check waits the configured delay before responding.
// Used by held-open-RPC drain tests to keep an RPC in-flight long
// enough to span a rotation.
type slowPeerCapturingHealthServer struct {
	healthpb.UnimplementedHealthServer
	serials *serialSet
	delay   time.Duration
}

func (h *slowPeerCapturingHealthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			if len(tlsInfo.State.PeerCertificates) > 0 {
				h.serials.add(tlsInfo.State.PeerCertificates[0].SerialNumber)
			}
		}
	}
	select {
	case <-time.After(h.delay):
		return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// startSlowMTLSHealthServer is startMTLSHealthServer with a per-Check
// delay so RPCs stay in flight long enough to observe drain semantics
// across rotation.
func startSlowMTLSHealthServer(t *testing.T, serverCertPath, serverKeyPath, caCertPath string, delay time.Duration) (*grpc.Server, string, *serialSet) {
	t.Helper()
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("server LoadX509KeyPair: %v", err)
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		t.Fatalf("read ca: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("server: empty CA bundle")
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	creds := credentials.NewTLS(tlsCfg)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(creds))
	serials := newSerialSet()
	healthpb.RegisterHealthServer(srv, &slowPeerCapturingHealthServer{serials: serials, delay: delay})
	go func() {
		_ = srv.Serve(lis)
	}()
	return srv, lis.Addr().String(), serials
}

// startMTLSHealthServer brings up a gRPC health server with mTLS
// require_client_auth ON, signed by the test CA. Returns the server,
// its bound address, and the serialSet capturing peer certs.
func startMTLSHealthServer(t *testing.T, serverCertPath, serverKeyPath, caCertPath string) (*grpc.Server, string, *serialSet) {
	t.Helper()
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("server LoadX509KeyPair: %v", err)
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		t.Fatalf("read ca: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("server: empty CA bundle")
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	creds := credentials.NewTLS(tlsCfg)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(creds))
	serials := newSerialSet()
	healthpb.RegisterHealthServer(srv, &peerCapturingHealthServer{serials: serials})
	go func() {
		_ = srv.Serve(lis)
	}()
	return srv, lis.Addr().String(), serials
}

// generateServerCertSignedBy issues a server cert with the given DNS
// SAN, signed by the test CA. Used by the e2e mTLS server.
func generateServerCertSignedBy(t *testing.T, dir, name, caCertPath, caKeyPath, serverName string) (certPath, keyPath string) {
	t.Helper()
	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParseECPrivateKey(caKeyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{serverName},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	if err := writePEM(certPath, "CERTIFICATE", der); err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func readCertSerial(t *testing.T, certPath string) *big.Int {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal(errors.New("no PEM block in cert file"))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return new(big.Int).Set(cert.SerialNumber)
}

// Avoid an unused import warning if a test branch evolves; make sure
// fmt is referenced even when no use-site needs it.
var _ = fmt.Sprintf
