// Package tlsutil gives the transporter a stable, self-signed TLS identity
// so the client<->transporter connection is encrypted in transit. There is
// no CA involved — the transporter isn't a public service with a domain to
// prove ownership of — so this only protects against passive eavesdropping,
// not an active man-in-the-middle sitting between a client and the
// transporter — unless the client pins the certificate's fingerprint (see
// Fingerprint), which the transporter prints on startup for that purpose.
// The ADB traffic itself doesn't depend on it: that is
// encrypted end to end between the owner and the guest (see client/e2e),
// under keys bound to the identities whose fingerprints the two compare.
package tlsutil

import (
	"adb-remote.maci.team/shared/certfingerprint"
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
	"time"
)

const certValidity = 10 * 365 * 24 * time.Hour

// LoadOrCreateCertificate loads a TLS certificate/key pair from certPath
// and keyPath, generating and persisting a new self-signed one if either
// file doesn't exist yet, so the transporter keeps the same TLS identity
// across restarts. created reports whether it generated a new one, which
// changes the fingerprint clients may have pinned.
func LoadOrCreateCertificate(certPath string, keyPath string) (cert tls.Certificate, created bool, err error) {
	cert, err = tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		return cert, false, nil
	}
	if !os.IsNotExist(err) {
		return tls.Certificate{}, false, err
	}

	certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, false, err
	}
	if err := persist(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, false, err
	}
	if err := persist(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, false, err
	}
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	return cert, err == nil, err
}

// Fingerprint returns an SSH-style "SHA256:<base64>" fingerprint of cert's
// leaf certificate (see shared/certfingerprint), which a client can pin
// with its transporterCertificateFingerprint setting.
func Fingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", errors.New("certificate has no DER bytes")
	}
	return certfingerprint.Of(cert.Certificate[0]), nil
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
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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
