package identity

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestLoadGeneratesAndPersistsAKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "identity")

	first, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %s", err)
	}
	if len(first.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("expected a %d byte public key, got %d", ed25519.PublicKeySize, len(first.PublicKey))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the key file to be created: %s", err)
	}

	second, err := Load(path)
	if err != nil {
		t.Fatalf("second Load failed: %s", err)
	}
	if !first.PublicKey.Equal(second.PublicKey) {
		t.Fatalf("expected the same identity to be loaded back, got a different public key")
	}
}

func TestLoadCreatesPrivateFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	if _, err := Load(path); err != nil {
		t.Fatalf("Load failed: %s", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %s", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected file mode 0600, got %o", perm)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, []byte("not a valid key"), 0o600); err != nil {
		t.Fatalf("failed to write the test fixture: %s", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected an error loading a corrupt identity file")
	}
}

var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func TestFingerprintFormat(t *testing.T) {
	identity, err := generate()
	if err != nil {
		t.Fatalf("generate failed: %s", err)
	}
	fingerprint := identity.Fingerprint()
	if !fingerprintPattern.MatchString(fingerprint) {
		t.Fatalf("expected a fingerprint matching %s, got %q", fingerprintPattern, fingerprint)
	}
}

func TestFingerprintIsDeterministicAndDistinct(t *testing.T) {
	a, err := generate()
	if err != nil {
		t.Fatalf("generate failed: %s", err)
	}
	b, err := generate()
	if err != nil {
		t.Fatalf("generate failed: %s", err)
	}

	if a.Fingerprint() != a.Fingerprint() {
		t.Fatalf("expected the fingerprint of the same key to be stable")
	}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatalf("expected two different keys to have different fingerprints")
	}
	if Fingerprint(a.PublicKey) != a.Fingerprint() {
		t.Fatalf("expected the package-level Fingerprint to match Identity.Fingerprint")
	}
}

func TestDefaultPathUnderHomeDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %s", err)
	}
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath failed: %s", err)
	}
	want := filepath.Join(home, ".adb-remote", "identity")
	if path != want {
		t.Fatalf("expected %q, got %q", want, path)
	}
}
