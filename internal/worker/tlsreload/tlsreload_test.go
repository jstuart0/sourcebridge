// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package tlsreload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestNew_InitialLoadValid verifies the happy path: a valid cert+key+CA
// loads cleanly and the cert is available via Cert().
func TestNew_InitialLoadValid(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api.sourcebridge.svc.cluster.local")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ServiceIdentity:   "api.sourcebridge.svc.cluster.local",
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if w.Cert() == nil {
		t.Fatal("expected Cert() to return non-nil after successful initial load")
	}
	if w.RootCAs() == nil {
		t.Fatal("expected RootCAs() to return non-nil after successful initial load")
	}
	if w.LoadSuccessCount() != 1 {
		t.Errorf("LoadSuccessCount: got %d, want 1", w.LoadSuccessCount())
	}
}

// TestNew_MissingChainErrors verifies that a cert that does not chain
// to the configured CA fails the initial load when ChainVerification
// is true.
func TestNew_MissingChainErrors(t *testing.T) {
	dir := t.TempDir()
	caCert, _ := genCA(t, dir, "real-ca")
	bogusCA, bogusKey := genCA(t, filepath.Join(dir, "bogus"), "bogus-ca")
	// Sign client cert with the bogus CA.
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", bogusCA, bogusKey, "api.sourcebridge.svc.cluster.local")

	_, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert, // doesn't match the cert's signer
		ChainVerification: true,
	})
	if err == nil {
		t.Fatal("expected error when cert does not chain to CA")
	}
	if !errors.Is(err, ErrChainVerifyFailed) {
		t.Errorf("expected ErrChainVerifyFailed, got %v", err)
	}
}

// TestNew_ExpiredCertErrors verifies expired cert is rejected.
func TestNew_ExpiredCertErrors(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	expiredCert, expiredKey := genClientCertExpired(t, dir, "expired", caCert, caKey)

	_, err := New(Config{
		CertPath:          expiredCert,
		KeyPath:           expiredKey,
		CAPath:            caCert,
		ChainVerification: false, // skip chain so we test the expiration path
	})
	if err == nil {
		t.Fatal("expected error for expired cert")
	}
	if !errors.Is(err, ErrCertExpired) {
		t.Errorf("expected ErrCertExpired, got %v", err)
	}
}

// TestNew_MissingClientAuthEKUErrors verifies an EKU mismatch is rejected.
func TestNew_MissingClientAuthEKUErrors(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	serverOnlyCert, serverOnlyKey := genClientCertWithEKU(t, dir, "server-only", caCert, caKey,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})

	_, err := New(Config{
		CertPath:          serverOnlyCert,
		KeyPath:           serverOnlyKey,
		CAPath:            caCert,
		ChainVerification: false,
	})
	if err == nil {
		t.Fatal("expected error for missing ClientAuth EKU")
	}
	if !errors.Is(err, ErrMissingClientAuth) {
		t.Errorf("expected ErrMissingClientAuth, got %v", err)
	}
}

// TestNew_ServiceIdentityMismatchErrors verifies SAN matching.
func TestNew_ServiceIdentityMismatchErrors(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "wrong-name.example.com")

	_, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ServiceIdentity:   "api.sourcebridge.svc.cluster.local",
		ChainVerification: true,
	})
	if err == nil {
		t.Fatal("expected error for SAN mismatch")
	}
	if !errors.Is(err, ErrServiceIdentityNoMatch) {
		t.Errorf("expected ErrServiceIdentityNoMatch, got %v", err)
	}
}

// TestNew_EmptyCABundleErrors verifies a CA file with no PEM is rejected.
func TestNew_EmptyCABundleErrors(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api")
	emptyCA := filepath.Join(dir, "empty-ca.crt")
	if err := os.WriteFile(emptyCA, []byte("not a pem"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            emptyCA,
		ChainVerification: false,
	})
	if err == nil {
		t.Fatal("expected error for empty CA bundle")
	}
	if !errors.Is(err, ErrEmptyCABundle) {
		t.Errorf("expected ErrEmptyCABundle, got %v", err)
	}
}

// TestReload_AfterValidNewCertSwapsAtomically verifies that calling
// Reload() after writing a new valid cert atomically swaps the active
// material AND fires an OnReload(true) callback.
func TestReload_AfterValidNewCertSwapsAtomically(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api.sourcebridge.svc.cluster.local")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ServiceIdentity:   "api.sourcebridge.svc.cluster.local",
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	originalCert := w.Cert()
	if originalCert == nil {
		t.Fatal("originalCert should not be nil")
	}

	// Capture callback invocations.
	var successCount, failureCount atomic.Int64
	w.OnReload(func(success bool, err error) {
		if success {
			successCount.Add(1)
		} else {
			failureCount.Add(1)
		}
	})
	// R3 followups B2: Reload no longer auto-Commits. Subscribe an
	// OnCandidate callback that immediately Commits — this preserves
	// the "Reload swaps the active cert" behavior the legacy test
	// asserted, while still demonstrating the new candidate fan-out.
	w.OnCandidate(func(c Candidate) {
		_, _ = w.Commit(c)
	})

	// Write a fresh valid cert+key to the same paths.
	newCert, newKey := genClientCertSignedBy(t, dir, "client-v2", caCert, caKey, "api.sourcebridge.svc.cluster.local")
	if err := os.Rename(newCert, clientCert); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newKey, clientKey); err != nil {
		t.Fatal(err)
	}

	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if w.Cert() == originalCert {
		t.Error("expected Cert() to return a different cert after Reload+Commit")
	}
	if w.LoadSuccessCount() != 2 {
		t.Errorf("LoadSuccessCount: got %d, want 2", w.LoadSuccessCount())
	}
	if successCount.Load() != 1 {
		t.Errorf("OnReload success: got %d, want 1", successCount.Load())
	}
	if failureCount.Load() != 0 {
		t.Errorf("OnReload failure: got %d, want 0", failureCount.Load())
	}
	if w.CommittedGeneration() != 2 {
		t.Errorf("CommittedGeneration: got %d, want 2 (init + reload commit)", w.CommittedGeneration())
	}
}

// TestReload_MalformedNewCertKeepsOldCert verifies that a corrupt
// reload leaves the previous material in place and fires an
// OnReload(false) callback.
func TestReload_MalformedNewCertKeepsOldCert(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ChainVerification: false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	originalCert := w.Cert()

	var failureCount atomic.Int64
	w.OnReload(func(success bool, err error) {
		if !success {
			failureCount.Add(1)
		}
	})

	// Corrupt the cert file.
	if err := os.WriteFile(clientCert, []byte("not a cert"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := w.Reload(); err == nil {
		t.Error("expected Reload to error on malformed cert")
	}

	if w.Cert() != originalCert {
		t.Error("expected Cert() to retain the original cert after a failed reload")
	}
	if failureCount.Load() != 1 {
		t.Errorf("OnReload failure: got %d, want 1", failureCount.Load())
	}
	if w.LoadFailureCount() == 0 {
		t.Error("LoadFailureCount should be > 0")
	}
}

// TestStart_FsNotifyTriggersReload verifies the fsnotify path: writing
// a new cert to the watched path causes loadAndSwap to fire without
// an explicit Reload() call. We give a short deadline because fsnotify
// + the 250ms debounce timer mean the swap won't be instant.
func TestStart_FsNotifyTriggersReload(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ChainVerification: false,
		PollInterval:      time.Hour, // disable polling for this test
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// R3 followups B2: Reload only Stages; subscribers Commit. Wire
	// an auto-Commit OnCandidate so the test sees the fsnotify-driven
	// path swap the active cert the way the legacy contract did.
	w.OnCandidate(func(c Candidate) {
		_, _ = w.Commit(c)
	})

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	originalCert := w.Cert()

	// Write a new cert (and key) to the same path; rename atomically.
	newCert, newKey := genClientCertSignedBy(t, dir, "client-v2", caCert, caKey, "api")
	if err := os.Rename(newCert, clientCert); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newKey, clientKey); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if w.Cert() != originalCert {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if w.Cert() == originalCert {
		t.Error("expected fsnotify-triggered reload+commit to swap the cert within 3s")
	}
}

// ─── R3 followups B2 — Stage/Commit + Candidate generation tests ───

// TestStage_ProducesStrictlyIncreasingGenerations pins that every
// Stage call produces a Generation strictly greater than the last,
// independent of whether prior candidates were Committed.
func TestStage_ProducesStrictlyIncreasingGenerations(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api.svc")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// Constructor's Stage+Commit raised both gens to 1.
	if got := w.StagedGeneration(); got != 1 {
		t.Errorf("post-init StagedGeneration: got %d, want 1", got)
	}

	c2, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if c2.Generation != 2 {
		t.Errorf("Stage gen: got %d, want 2", c2.Generation)
	}

	c3, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if c3.Generation != 3 {
		t.Errorf("Stage gen: got %d, want 3", c3.Generation)
	}

	// CommittedGeneration unchanged because nothing was Committed.
	if got := w.CommittedGeneration(); got != 1 {
		t.Errorf("CommittedGeneration after 2 Stages-no-Commit: got %d, want 1", got)
	}
}

// TestStage_WithoutCommit_PreservesActiveMaterial pins the core B2
// invariant: a Stage that is never Committed leaves Cert() and
// RootCAs() at the previously-committed values.
func TestStage_WithoutCommit_PreservesActiveMaterial(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api.svc")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	originalCert := w.Cert()
	originalPool := w.RootCAs()

	// Replace cert files with v2 material, Stage but don't Commit.
	newCert, newKey := genClientCertSignedBy(t, dir, "client-v2", caCert, caKey, "api.svc")
	if err := os.Rename(newCert, clientCert); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newKey, clientKey); err != nil {
		t.Fatal(err)
	}

	cand, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	_ = cand // we deliberately do NOT Commit

	if w.Cert() != originalCert {
		t.Error("Stage without Commit must NOT change Cert()")
	}
	if w.RootCAs() != originalPool {
		t.Error("Stage without Commit must NOT change RootCAs()")
	}
	if got := w.CommittedGeneration(); got != 1 {
		t.Errorf("CommittedGeneration: got %d, want 1 (unchanged)", got)
	}
}

// TestCommit_StaleCandidate_ReturnsOkFalseWithoutMutation pins that a
// candidate whose Generation is less than the current committedGen
// is dropped without changing active material.
func TestCommit_StaleCandidate_ReturnsOkFalseWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api.svc")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// Stage two candidates from two file rotations.
	newCert, newKey := genClientCertSignedBy(t, dir, "client-v2", caCert, caKey, "api.svc")
	if err := os.Rename(newCert, clientCert); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newKey, clientKey); err != nil {
		t.Fatal(err)
	}
	c2, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage v2: %v", err)
	}

	newCert3, newKey3 := genClientCertSignedBy(t, dir, "client-v3", caCert, caKey, "api.svc")
	if err := os.Rename(newCert3, clientCert); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newKey3, clientKey); err != nil {
		t.Fatal(err)
	}
	c3, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage v3: %v", err)
	}

	// Commit v3 first (its probe wins the race).
	ok, err := w.Commit(c3)
	if err != nil {
		t.Fatalf("Commit v3: %v", err)
	}
	if !ok {
		t.Error("Commit v3: expected ok=true")
	}

	// Now v2's "probe" finally returns and tries to Commit. It should
	// be dropped as stale.
	v3Cert := w.Cert()
	staleOk, staleErr := w.Commit(c2)
	if staleErr != nil {
		t.Errorf("Commit stale should return nil err, got %v", staleErr)
	}
	if staleOk {
		t.Error("Commit stale should return ok=false")
	}
	if w.Cert() != v3Cert {
		t.Error("stale Commit must NOT replace the active cert")
	}
	if got := w.CommittedGeneration(); got != c3.Generation {
		t.Errorf("CommittedGeneration: got %d, want %d", got, c3.Generation)
	}
	if got := w.CommitStaleCount(); got != 1 {
		t.Errorf("CommitStaleCount: got %d, want 1", got)
	}
}

// TestCommit_IdempotentForActiveCandidate pins that re-committing the
// currently-active candidate (same Generation, same pointers) is a
// no-op that returns ok=true.
func TestCommit_IdempotentForActiveCandidate(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api.svc")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// Stage a v2 candidate, Commit it, then Commit it again.
	newCert, newKey := genClientCertSignedBy(t, dir, "client-v2", caCert, caKey, "api.svc")
	if err := os.Rename(newCert, clientCert); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newKey, clientKey); err != nil {
		t.Fatal(err)
	}
	cand, err := w.Stage()
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	ok1, err := w.Commit(cand)
	if err != nil || !ok1 {
		t.Fatalf("first Commit: ok=%v err=%v", ok1, err)
	}
	commitsAfterFirst := w.CommitCount()

	ok2, err := w.Commit(cand)
	if err != nil {
		t.Errorf("second Commit (idempotent): err=%v", err)
	}
	if !ok2 {
		t.Error("second Commit (idempotent): ok should be true")
	}
	if w.CommitCount() != commitsAfterFirst {
		t.Errorf("idempotent re-commit must not increment CommitCount: %d → %d",
			commitsAfterFirst, w.CommitCount())
	}
}

// TestOnCandidate_FiresOutsideMutexes pins that a callback can call
// Commit synchronously without deadlocking. If the watcher held
// reloadMu or commitMu while invoking callbacks, this test would
// deadlock.
func TestOnCandidate_FiresOutsideMutexes(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t, dir, "test-ca")
	clientCert, clientKey := genClientCertSignedBy(t, dir, "client", caCert, caKey, "api.svc")

	w, err := New(Config{
		CertPath:          clientCert,
		KeyPath:           clientKey,
		CAPath:            caCert,
		ChainVerification: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var committed atomic.Bool
	w.OnCandidate(func(c Candidate) {
		ok, err := w.Commit(c)
		if err != nil {
			t.Errorf("synchronous Commit from callback: %v", err)
		}
		if ok {
			committed.Store(true)
		}
	})

	// File rotation + Reload triggers the candidate fan-out.
	newCert, newKey := genClientCertSignedBy(t, dir, "client-v2", caCert, caKey, "api.svc")
	if err := os.Rename(newCert, clientCert); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newKey, clientKey); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- w.Reload() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Reload error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Reload deadlocked — callback held reloadMu or commitMu")
	}

	if !committed.Load() {
		t.Error("synchronous callback Commit did not succeed")
	}
}

// ─── helpers (purpose-built for this package; client_tls_test.go has
// its own copies in the worker package and we don't want to import
// across test boundaries) ─────────────────────────────────────────

func genCA(t *testing.T, dir, name string) (caCertPath, caKeyPath string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca genkey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("ca create: %v", err)
	}
	caCertPath = filepath.Join(dir, "ca.crt")
	caKeyPath = filepath.Join(dir, "ca.key")
	writePEMOrFatal(t, caCertPath, "CERTIFICATE", der)
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	writePEMOrFatal(t, caKeyPath, "EC PRIVATE KEY", keyBytes)
	return caCertPath, caKeyPath
}

func genClientCertSignedBy(t *testing.T, dir, name, caCertPath, caKeyPath, dnsName string) (certPath, keyPath string) {
	t.Helper()
	return genClientCertWithEKUAndDNS(t, dir, name, caCertPath, caKeyPath,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		dnsName,
		time.Now().Add(-time.Hour),
		time.Now().Add(24*time.Hour))
}

func genClientCertExpired(t *testing.T, dir, name, caCertPath, caKeyPath string) (certPath, keyPath string) {
	t.Helper()
	return genClientCertWithEKUAndDNS(t, dir, name, caCertPath, caKeyPath,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		"api",
		time.Now().Add(-2*time.Hour),
		time.Now().Add(-time.Hour))
}

func genClientCertWithEKU(t *testing.T, dir, name, caCertPath, caKeyPath string, eku []x509.ExtKeyUsage) (certPath, keyPath string) {
	t.Helper()
	return genClientCertWithEKUAndDNS(t, dir, name, caCertPath, caKeyPath, eku, "api",
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
}

func genClientCertWithEKUAndDNS(t *testing.T, dir, name, caCertPath, caKeyPath string, eku []x509.ExtKeyUsage, dnsName string, notBefore, notAfter time.Time) (certPath, keyPath string) {
	t.Helper()

	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		t.Fatalf("read ca cert: %v", err)
	}
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse ca cert: %v", err)
	}
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		t.Fatalf("read ca key: %v", err)
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParseECPrivateKey(caKeyBlock.Bytes)
	if err != nil {
		t.Fatalf("parse ca key: %v", err)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("client genkey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     []string{dnsName},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	writePEMOrFatal(t, certPath, "CERTIFICATE", der)
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	writePEMOrFatal(t, keyPath, "EC PRIVATE KEY", keyBytes)
	return certPath, keyPath
}

func writePEMOrFatal(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}
