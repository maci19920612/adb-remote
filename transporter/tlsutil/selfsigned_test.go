package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestLoadOrCreateCertificateGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	first, err := LoadOrCreateCertificate(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateCertificate failed: %s", err)
	}
	if len(first.Certificate) == 0 {
		t.Fatalf("expected at least one certificate in the chain")
	}

	second, err := LoadOrCreateCertificate(certPath, keyPath)
	if err != nil {
		t.Fatalf("second LoadOrCreateCertificate failed: %s", err)
	}
	if string(first.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatalf("expected the same certificate to be loaded back on the second call")
	}
}

func TestLoadOrCreateCertificateNestedDirectory(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "sub", "cert.pem")
	keyPath := filepath.Join(dir, "sub", "key.pem")

	if _, err := LoadOrCreateCertificate(certPath, keyPath); err != nil {
		t.Fatalf("LoadOrCreateCertificate failed: %s", err)
	}
}

func TestGeneratedCertificateIsValidAndUsableForTLS(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	cert, err := LoadOrCreateCertificate(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateCertificate failed: %s", err)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("failed to parse the generated certificate: %s", err)
	}
	if time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		t.Fatalf("expected the certificate to currently be valid, got NotBefore=%s NotAfter=%s", leaf.NotBefore, leaf.NotAfter)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("failed to start a TLS listener with the generated certificate: %s", err)
	}
	defer listener.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		accepted <- conn.(*tls.Conn).Handshake()
	}()

	clientConn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("client dial failed: %s", err)
	}
	defer clientConn.Close()

	if err := <-accepted; err != nil {
		t.Fatalf("server-side handshake failed: %s", err)
	}
}

var certFingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func TestFingerprintFormat(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadOrCreateCertificate(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatalf("LoadOrCreateCertificate failed: %s", err)
	}
	fingerprint, err := Fingerprint(cert)
	if err != nil {
		t.Fatalf("Fingerprint failed: %s", err)
	}
	if !certFingerprintPattern.MatchString(fingerprint) {
		t.Fatalf("expected a fingerprint matching %s, got %q", certFingerprintPattern, fingerprint)
	}
}

func TestFingerprintRejectsEmptyCertificate(t *testing.T) {
	if _, err := Fingerprint(tls.Certificate{}); err == nil {
		t.Fatalf("expected an error for a certificate with no DER bytes")
	}
}
