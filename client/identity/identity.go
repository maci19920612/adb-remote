// Package identity gives each client installation a stable, persistent
// Ed25519 keypair, independent of the transporter and independent of any
// TLS on that connection. Its public key is sent along with a room join
// request; the fingerprint (see Fingerprint) is what a human displays and
// compares out of band (voice call, chat, in person) to verify they're
// actually talking to who they think they are — the transporter is a
// relay, not a trusted party, so this check is what actually protects
// against a compromised or malicious relay silently substituting a
// different guest.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// Identity is a client's persistent Ed25519 keypair.
type Identity struct {
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

// Fingerprint returns a human-comparable fingerprint of publicKey, in the
// same "SHA256:<base64, no padding>" format OpenSSH uses for host/identity
// key fingerprints.
func Fingerprint(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Fingerprint returns this identity's own fingerprint.
func (i *Identity) Fingerprint() string {
	return Fingerprint(i.PublicKey)
}

// DefaultPath returns the standard location for the persisted identity key:
// $HOME/.adb-remote/identity (analogous to an SSH key under ~/.ssh).
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".adb-remote", "identity"), nil
}

// Load reads the identity key at path, generating and persisting a new one
// if it doesn't exist yet. The key file (and its parent directory, if
// Load creates it) are written with owner-only permissions, since it's a
// private key.
func Load(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return decode(data)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	identity, err := generate()
	if err != nil {
		return nil, err
	}
	if err := persist(path, identity); err != nil {
		return nil, err
	}
	return identity, nil
}

func generate() (*Identity, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{PublicKey: publicKey, PrivateKey: privateKey}, nil
}

func persist(path string, identity *Identity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, identity.PrivateKey, 0o600)
}

func decode(data []byte) (*Identity, error) {
	if len(data) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid identity key file: expected %d bytes, got %d", ed25519.PrivateKeySize, len(data))
	}
	privateKey := ed25519.PrivateKey(data)
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("invalid identity key file: could not derive the public key")
	}
	return &Identity{PublicKey: publicKey, PrivateKey: privateKey}, nil
}
