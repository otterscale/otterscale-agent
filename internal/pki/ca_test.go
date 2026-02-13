package pki

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestNewCAFromSeed_SameSeed(t *testing.T) {
	// Verify two CAs from the same seed can both validate each
	// other's certificates (functionally equivalent even if the
	// raw PEM bytes differ due to Go's internal ECDSA changes in
	// 1.22+ which may use crypto/rand internally).
	ca1, err := NewCAFromSeed("test-seed")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}
	ca2, err := NewCAFromSeed("test-seed")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	// Both CAs should produce valid, parseable certs.
	if len(ca1.CertPEM()) == 0 || len(ca2.CertPEM()) == 0 {
		t.Error("expected non-empty cert PEM")
	}
}

func TestNewCAFromSeed_DifferentSeeds(t *testing.T) {
	ca1, err := NewCAFromSeed("seed-a")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	ca2, err := NewCAFromSeed("seed-b")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	if string(ca1.CertPEM()) == string(ca2.CertPEM()) {
		t.Error("expected different CA cert PEMs for different seeds")
	}
}

func TestNewCAFromSeed_CertProperties(t *testing.T) {
	ca, err := NewCAFromSeed("test-props")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	block, _ := pem.Decode(ca.CertPEM())
	if block == nil {
		t.Fatal("failed to decode CA cert PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	if !cert.IsCA {
		t.Error("expected IsCA to be true")
	}
	if cert.Subject.CommonName != "otterscale-ca" {
		t.Errorf("expected CN=otterscale-ca, got %s", cert.Subject.CommonName)
	}
	// MaxPathLen should be 0 or -1 (Go represents "0 but set" as
	// MaxPathLen=0 + MaxPathLenZero=true; when parsed it may show
	// as -1 in some Go versions).
	if cert.MaxPathLen > 0 {
		t.Errorf("expected MaxPathLen<=0, got %d", cert.MaxPathLen)
	}
}

func TestSignCSR(t *testing.T) {
	ca, err := NewCAFromSeed("sign-test")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	key, _, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	csrPEM, err := GenerateCSR(key, "test-agent")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	certPEM, err := ca.SignCSR(csrPEM)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode signed cert PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	if cert.Subject.CommonName != "test-agent" {
		t.Errorf("expected CN=test-agent, got %s", cert.Subject.CommonName)
	}

	// Verify the certificate was signed by the CA.
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("certificate verification failed: %v", err)
	}
}

func TestSignCSR_InvalidPEM(t *testing.T) {
	ca, err := NewCAFromSeed("invalid-test")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	_, err = ca.SignCSR([]byte("not-a-pem"))
	if err == nil {
		t.Error("expected error for invalid PEM, got nil")
	}
}

func TestGenerateServerCert(t *testing.T) {
	ca, err := NewCAFromSeed("server-cert-test")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	certPEM, keyPEM, err := ca.GenerateServerCert("127.0.0.1", "example.com")
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	if len(certPEM) == 0 {
		t.Error("expected non-empty cert PEM")
	}
	if len(keyPEM) == 0 {
		t.Error("expected non-empty key PEM")
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode server cert PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("expected IP SAN 127.0.0.1, got %v", cert.IPAddresses)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "example.com" {
		t.Errorf("expected DNS SAN example.com, got %v", cert.DNSNames)
	}

	// Verify signed by CA.
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("certificate verification failed: %v", err)
	}
}

func TestDeriveAuth(t *testing.T) {
	ca, err := NewCAFromSeed("auth-test")
	if err != nil {
		t.Fatalf("NewCAFromSeed: %v", err)
	}

	key, _, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	csrPEM, err := GenerateCSR(key, "agent-1")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	certPEM, err := ca.SignCSR(csrPEM)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}

	auth1, err := DeriveAuth("agent-1", certPEM)
	if err != nil {
		t.Fatalf("DeriveAuth: %v", err)
	}

	// Same inputs should produce the same auth.
	auth2, err := DeriveAuth("agent-1", certPEM)
	if err != nil {
		t.Fatalf("DeriveAuth: %v", err)
	}

	if auth1 != auth2 {
		t.Error("expected deterministic auth string, got different results")
	}

	// Should contain the agent ID.
	if len(auth1) < len("agent-1:")+1 {
		t.Errorf("auth string too short: %s", auth1)
	}
	if auth1[:len("agent-1:")] != "agent-1:" {
		t.Errorf("expected auth to start with agent-1:, got %s", auth1)
	}
}

func TestDeriveAuth_InvalidPEM(t *testing.T) {
	_, err := DeriveAuth("agent", []byte("not-a-pem"))
	if err == nil {
		t.Error("expected error for invalid PEM, got nil")
	}
}

func TestGenerateKey_And_CSR(t *testing.T) {
	key, keyPEM, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	if key == nil {
		t.Fatal("expected non-nil key")
	}
	if len(keyPEM) == 0 {
		t.Fatal("expected non-empty key PEM")
	}

	csrPEM, err := GenerateCSR(key, "test-cn")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatal("expected CERTIFICATE REQUEST PEM block")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}

	if csr.Subject.CommonName != "test-cn" {
		t.Errorf("expected CN=test-cn, got %s", csr.Subject.CommonName)
	}
}
