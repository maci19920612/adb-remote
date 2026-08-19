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
