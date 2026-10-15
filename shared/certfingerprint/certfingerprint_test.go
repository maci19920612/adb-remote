package certfingerprint

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"testing"
)

var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func TestOfFormat(t *testing.T) {
	der := []byte("certificate bytes")
	fingerprint := Of(der)
	if !fingerprintPattern.MatchString(fingerprint) {
		t.Fatalf("expected a fingerprint matching %s, got %q", fingerprintPattern, fingerprint)
	}
	sum := sha256.Sum256(der)
	if fingerprint != "SHA256:"+base64.RawStdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("expected the SHA-256 of the certificate, got %q", fingerprint)
	}
}

func TestParseRoundTrips(t *testing.T) {
	der := []byte("certificate bytes")
	digest, err := Parse(Of(der))
	if err != nil {
		t.Fatalf("Parse failed: %s", err)
	}
	if digest != Digest(der) {
		t.Fatalf("expected the parsed digest to match the certificate's")
	}
}

// TestParseToleratesSurroundingWhitespace covers a fingerprint pasted from
// the transporter's log with a stray space or newline.
func TestParseToleratesSurroundingWhitespace(t *testing.T) {
	der := []byte("certificate bytes")
	if _, err := Parse("  " + Of(der) + "\n"); err != nil {
		t.Fatalf("expected surrounding whitespace to be ignored, got %s", err)
	}
}

func TestParseRejectsMalformedFingerprints(t *testing.T) {
	valid := Of([]byte("certificate bytes"))
	for _, fingerprint := range []string{
		"",
		valid[len("SHA256:"):],
		"SHA1:" + valid[len("SHA256:"):],
		valid[:len(valid)-1],
		valid + "A",
		"SHA256:!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!",
	} {
		if _, err := Parse(fingerprint); err == nil {
			t.Fatalf("expected %q to be rejected", fingerprint)
		}
	}
}
