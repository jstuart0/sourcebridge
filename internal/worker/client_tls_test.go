// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package worker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNew_TLSDisabled_DefaultsToInsecure verifies the legacy path is
// untouched when TLS is disabled (zero-value TLSConfig).
func TestNew_TLSDisabled_DefaultsToInsecure(t *testing.T) {
	c, err := New("localhost:59999", TLSConfig{}) // Enabled = false
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	if c.Address() != "localhost:59999" {
		t.Errorf("Address: got %q, want localhost:59999", c.Address())
	}
}

// TestNew_TLSEnabledWithMissingPaths_ReturnsError pins the fail-closed
// behavior: enabling TLS without supplying all three paths must error
// at construction time rather than silently fall back to insecure.
func TestNew_TLSEnabledWithMissingPaths_ReturnsError(t *testing.T) {
	cases := []TLSConfig{
		{Enabled: true}, // all paths empty
		{Enabled: true, CertPath: "/tmp/x"},
		{Enabled: true, CertPath: "/tmp/x", KeyPath: "/tmp/y"},
	}
	for i, cfg := range cases {
		_, err := New("localhost:59999", cfg)
		if err == nil {
			t.Errorf("case %d: New with incomplete TLS config should error", i)
		}
	}
}

// TestNew_TLSEnabledWithBogusCAPath_ReturnsError pins that an
// unreadable / malformed CA bundle errors at construction time.
func TestNew_TLSEnabledWithBogusCAPath_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	cert, key := generateTestKeyPair(t, dir, "client")

	_, err := New("localhost:59999", TLSConfig{
		Enabled:  true,
		CertPath: cert,
		KeyPath:  key,
		CAPath:   "/nonexistent/ca.crt",
	})
	if err == nil {
		t.Fatal("expected error for missing CA path")
	}
}

// TestNew_TLSEnabledWithValidPaths_LoadsCredentials walks a complete
// happy-path: generate a self-signed test CA, sign a client cert, point
// the New() function at them. The dial succeeds (no real worker is
// listening; the client is lazy-connect, so success here means the TLS
// material loaded cleanly).
func TestNew_TLSEnabledWithValidPaths_LoadsCredentials(t *testing.T) {
	dir := t.TempDir()
	caCertPath, caKeyPath := generateTestCA(t, dir)
	clientCert, clientKey := generateClientCertSignedBy(t, dir, "client", caCertPath, caKeyPath)

	c, err := New("localhost:59999", TLSConfig{
		Enabled:    true,
		CertPath:   clientCert,
		KeyPath:    clientKey,
		CAPath:     caCertPath,
		ServerName: "worker.sourcebridge.svc.cluster.local",
	})
	if err != nil {
		t.Fatalf("New with valid TLS: %v", err)
	}
	defer c.Close()
	if c.Address() != "localhost:59999" {
		t.Errorf("Address: got %q, want localhost:59999", c.Address())
	}
}

// ─── Test helpers ────────────────────────────────────────────────────

// generateTestKeyPair writes a self-signed test cert + key to dir and
// returns their paths. Useful for wiring up TLS dial paths without
// needing a real CA.
func generateTestKeyPair(t *testing.T, dir, name string) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	if err := writePEM(certPath, "CERTIFICATE", derBytes); err != nil {
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

// generateTestCA generates a self-signed CA cert + key for signing
// client certs in tests.
func generateTestCA(t *testing.T, dir string) (caCertPath, caKeyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca genkey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
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
	if err := writePEM(caCertPath, "CERTIFICATE", der); err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePEM(caKeyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		t.Fatal(err)
	}
	return caCertPath, caKeyPath
}

// generateClientCertSignedBy issues a client cert signed by the given
// test CA. Returns paths to the cert and key.
func generateClientCertSignedBy(t *testing.T, dir, name, caCertPath, caKeyPath string) (certPath, keyPath string) {
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
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
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

func writePEM(path, blockType string, der []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
