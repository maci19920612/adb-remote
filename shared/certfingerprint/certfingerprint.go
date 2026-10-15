// Package certfingerprint computes and parses the fingerprint of the
// transporter's TLS certificate: the transporter prints it on startup, and
// a client configured with it accepts only that certificate (certificate
// pinning). Keeping both sides on this one package keeps the format they
// agree on in one place.
package certfingerprint

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// prefix names the hash, the way OpenSSH fingerprints do.
const prefix = "SHA256:"

// Digest returns the SHA-256 hash of a DER-encoded certificate.
func Digest(der []byte) [sha256.Size]byte {
	return sha256.Sum256(der)
}

// Of returns the fingerprint of a DER-encoded certificate in the
// "SHA256:<base64, no padding>" format.
func Of(der []byte) string {
	digest := Digest(der)
	return prefix + base64.RawStdEncoding.EncodeToString(digest[:])
}

// Parse returns the digest a fingerprint in Of's format stands for,
// ignoring surrounding whitespace.
func Parse(fingerprint string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	encoded, ok := strings.CutPrefix(strings.TrimSpace(fingerprint), prefix)
	if !ok {
		return digest, fmt.Errorf("invalid certificate fingerprint %q: expected %q followed by the base64 SHA-256 hash", fingerprint, prefix)
	}
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return digest, fmt.Errorf("invalid certificate fingerprint %q: %w", fingerprint, err)
	}
	if len(decoded) != sha256.Size {
		return digest, fmt.Errorf("invalid certificate fingerprint %q: expected a %d byte hash, got %d bytes", fingerprint, sha256.Size, len(decoded))
	}
	copy(digest[:], decoded)
	return digest, nil
}
