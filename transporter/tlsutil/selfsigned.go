// Package tlsutil gives the transporter a stable, self-signed TLS identity
// so the client<->transporter connection is encrypted in transit. There is
// no CA involved — the transporter isn't a public service with a domain to
// prove ownership of — so this only protects against passive eavesdropping,
// not an active man-in-the-middle sitting between a client and the
// transporter. Verifying who you're actually talking to end-to-end is what
// client/identity's public-key fingerprints are for.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const certValidity = 10 * 365 * 24 * time.Hour

// LoadOrCreateCertificate loads a TLS certificate/key pair from certPath
// and keyPath, generating and persisting a new self-signed one if either
// file doesn't exist yet, so the transporter keeps the same TLS identity
// across restarts.
func LoadOrCreateCertificate(certPath string, keyPath string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		return cert, nil
	}
	if !os.IsNotExist(err) {
		return tls.Certificate{}, err
	}

	certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := persist(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := persist(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

// Fingerprint returns an SSH-style "SHA256:<base64>" fingerprint of cert's
// leaf certificate, so an operator can publish it and a curious user can
// manually verify they're talking to the expected transporter (the client
// itself doesn't pin or check this — see the package doc).
func Fingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", errors.New("certificate has no DER bytes")
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

func generateSelfSigned() (certPEM []byte, keyPEM []byte, err error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: "adb-remote-transporter"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM, nil
}

func persist(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create the directory for %s: %w", path, err)
	}
	return os.WriteFile(path, data, perm)
}
