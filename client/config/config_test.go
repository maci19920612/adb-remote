package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write the test config file: %s", err)
	}
	return path
}

func TestLoadConfigParsesTransporterAddress(t *testing.T) {
	path := writeConfigFile(t, `{"transporterAddress": "127.0.0.1:9000"}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %s", err)
	}
	if config.TransporterAddress != "127.0.0.1:9000" {
		t.Fatalf("expected address %q, got %q", "127.0.0.1:9000", config.TransporterAddress)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatalf("expected an error for a missing config file")
	}
}

func TestLoadConfigInvalidJson(t *testing.T) {
	path := writeConfigFile(t, `not json`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatalf("expected an error for invalid JSON")
	}
}

const testFingerprint = "SHA256:Uq3ld0dKK7ytd0IA3YdeS2xAkTwpsUU4i3Se27ip8sw"

func TestLoadConfigCertificatePinningIsOptional(t *testing.T) {
	config, err := LoadConfig(writeConfigFile(t, `{"transporterAddress": "127.0.0.1:9000"}`))
	if err != nil {
		t.Fatalf("LoadConfig failed: %s", err)
	}
	if config.TransporterCertificateFingerprint != "" {
		t.Fatalf("expected no pinned certificate, got %q", config.TransporterCertificateFingerprint)
	}
}

func TestLoadConfigParsesThePinnedCertificateFingerprint(t *testing.T) {
	config, err := LoadConfig(writeConfigFile(t, `{"transporterAddress": "127.0.0.1:9000", "transporterCertificateFingerprint": "`+testFingerprint+`"}`))
	if err != nil {
		t.Fatalf("LoadConfig failed: %s", err)
	}
	if config.TransporterCertificateFingerprint != testFingerprint {
		t.Fatalf("expected the pinned fingerprint %q, got %q", testFingerprint, config.TransporterCertificateFingerprint)
	}
}

// TestLoadConfigRejectsAMalformedFingerprint checks a typo in the pinned
// fingerprint stops the client instead of silently turning pinning off.
func TestLoadConfigRejectsAMalformedFingerprint(t *testing.T) {
	path := writeConfigFile(t, `{"transporterAddress": "127.0.0.1:9000", "transporterCertificateFingerprint": "SHA256:not-a-fingerprint"}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatalf("expected an error for a malformed certificate fingerprint")
	}
}
