// Package pki provides a minimal Certificate Authority for issuing
// short-lived TLS certificates used in agent-to-server mTLS tunnels.
//
// The CA can be created deterministically from a seed string so that
// restarts produce the same CA certificate, keeping previously issued
// agent certificates valid until they expire.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// certValidity is the default validity period for agent certificates
// signed by the CA. Short-lived certificates limit the blast radius
// of a compromised key and avoid the need for explicit revocation.
const certValidity = 24 * time.Hour

// CA holds a self-signed certificate authority key pair and provides
// methods for signing CSRs and generating server certificates.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

// NewCAFromSeed creates a deterministic CA from the given seed string.
// The same seed always produces the same CA key pair and certificate,
// which is important for server restarts: agents that already hold a
// certificate signed by this CA can reconnect without re-registering.
func NewCAFromSeed(seed string) (*CA, error) {
	key, err := deriveKey(seed, "ca")
	if err != nil {
		return nil, fmt.Errorf("pki: derive CA key: %w", err)
	}

	serial := deriveSerial(seed, "ca-serial")

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"otterscale"},
			CommonName:   "otterscale-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create CA cert: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	return &CA{cert: cert, key: key, certPEM: certPEM}, nil
}

// CertPEM returns the PEM-encoded CA certificate. Agents use this to
// verify the tunnel server's identity and to be verified themselves.
func (ca *CA) CertPEM() []byte {
	return ca.certPEM
}

// SignCSR validates a PEM-encoded PKCS#10 certificate signing request
// and returns a PEM-encoded X.509 certificate signed by the CA. The
// certificate is valid for the default certValidity period.
func (ca *CA) SignCSR(csrPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("pki: invalid CSR PEM")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CSR: %w", err)
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: CSR signature invalid: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      csr.Subject,
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("pki: sign certificate: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), nil
}

// GenerateServerCert creates a TLS server certificate signed by the
// CA. The hosts parameter accepts IP addresses and DNS names that are
// added as Subject Alternative Names.
func (ca *CA) GenerateServerCert(hosts ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate server key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"otterscale"},
			CommonName:   "otterscale-tunnel",
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: create server cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: marshal server key: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// GenerateKey creates a new ECDSA P-256 private key suitable for use
// in a CSR. It returns the key and its PEM encoding.
func GenerateKey() (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate key: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: marshal key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return key, keyPEM, nil
}

// GenerateCSR creates a PEM-encoded PKCS#10 certificate signing
// request with the given common name.
func GenerateCSR(key *ecdsa.PrivateKey, cn string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"otterscale"},
			CommonName:   cn,
		},
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create CSR: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), nil
}

// DeriveAuth deterministically computes a chisel auth string
// ("user:password") from the agent ID and a signed certificate.
// Both the server (which signed the cert) and the agent (which
// received the cert) can independently compute this value.
func DeriveAuth(agentID string, certPEM []byte) string {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return agentID + ":"
	}
	h := sha256.Sum256(block.Bytes)
	pass := base64.RawURLEncoding.EncodeToString(h[:24])
	return agentID + ":" + pass
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// deriveKey deterministically produces an ECDSA P-256 private key
// from a seed and a label using SHA-256. This is intentionally simple:
// the seed should already have sufficient entropy.
func deriveKey(seed, label string) (*ecdsa.PrivateKey, error) {
	h := sha256.Sum256([]byte(label + ":" + seed))
	key, err := ecdsa.GenerateKey(elliptic.P256(), newDeterministicReader(h[:]))
	if err != nil {
		return nil, err
	}
	return key, nil
}

// deriveSerial produces a deterministic positive big.Int from a seed
// and label, suitable for use as a certificate serial number.
func deriveSerial(seed, label string) *big.Int {
	h := sha256.Sum256([]byte(label + ":" + seed))
	serial := new(big.Int).SetBytes(h[:16])
	serial.Abs(serial)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial
}

// randomSerial generates a cryptographically random serial number.
func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("pki: generate serial: %w", err)
	}
	return serial, nil
}

// deterministicReader is an io.Reader that expands a 32-byte seed
// into an arbitrary-length deterministic byte stream using SHA-256 in
// counter mode. It is used only for key derivation from a seed.
type deterministicReader struct {
	seed    [32]byte
	counter uint64
	buf     []byte
}

func newDeterministicReader(seed []byte) *deterministicReader {
	r := &deterministicReader{}
	copy(r.seed[:], seed)
	return r
}

func (r *deterministicReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(r.buf) == 0 {
			// Expand seed with counter.
			var block [40]byte
			copy(block[:32], r.seed[:])
			block[32] = byte(r.counter >> 56)
			block[33] = byte(r.counter >> 48)
			block[34] = byte(r.counter >> 40)
			block[35] = byte(r.counter >> 32)
			block[36] = byte(r.counter >> 24)
			block[37] = byte(r.counter >> 16)
			block[38] = byte(r.counter >> 8)
			block[39] = byte(r.counter)
			h := sha256.Sum256(block[:])
			r.buf = h[:]
			r.counter++
		}
		copied := copy(p[n:], r.buf)
		r.buf = r.buf[copied:]
		n += copied
	}
	return n, nil
}
