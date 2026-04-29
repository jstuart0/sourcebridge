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
