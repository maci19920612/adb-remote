// Package testtls generates ephemeral, in-memory self-signed TLS
// certificates for tests that need to stand in for the transporter's real
// (persisted) self-signed certificate — see transporter/tlsutil for the
// production equivalent. Not for production use.
package testtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// Certificate returns a freshly generated self-signed certificate valid for
// the current time, suitable for tls.Config.Certificates in a test-only TLS
// listener.
func Certificate(t *testing.T) tls.Certificate {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("testtls: failed to generate a private key: %s", err)
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("testtls: failed to generate a serial number: %s", err)
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("testtls: failed to create the certificate: %s", err)
	}

	cert, err := tls.X509KeyPair(encodeCert(derBytes), encodeKey(t, privateKey))
	if err != nil {
		t.Fatalf("testtls: failed to build the tls.Certificate: %s", err)
	}
	return cert
}

// Listen starts a TLS listener on 127.0.0.1:0 using an ephemeral
// self-signed certificate, and registers its cleanup with t.
func Listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{Certificate(t)}})
	if err != nil {
		t.Fatalf("testtls: failed to start a TLS listener: %s", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// Accept accepts the next connection on listener and completes its TLS
// handshake before returning. tls.Listener.Accept alone does not do this —
// the handshake is otherwise lazy, deferred to the connection's first
// Read/Write — which deadlocks a test where the client dials with the
// synchronous tls.Dial (which *does* block for the full handshake) before
// the test ever reads/writes on the accepted server-side connection.
func Accept(listener net.Listener) (net.Conn, error) {
	conn, err := listener.Accept()
	if err != nil {
		return nil, err
	}
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func encodeCert(derBytes []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
}

func encodeKey(t *testing.T, privateKey *ecdsa.PrivateKey) []byte {
	t.Helper()
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("testtls: failed to marshal the private key: %s", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
}
