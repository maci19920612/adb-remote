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

func TestCreateConfigParsesAddress(t *testing.T) {
	path := writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000"}`)
	config, err := CreateConfig(path)
	if err != nil {
		t.Fatalf("CreateConfig failed: %s", err)
	}
	if config.Address != "0.0.0.0:9000" {
		t.Fatalf("expected address %q, got %q", "0.0.0.0:9000", config.Address)
	}
}

func TestCreateConfigMissingFile(t *testing.T) {
	if _, err := CreateConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatalf("expected an error for a missing config file")
	}
}

func TestCreateConfigInvalidJson(t *testing.T) {
	path := writeConfigFile(t, `not json`)
	if _, err := CreateConfig(path); err == nil {
		t.Fatalf("expected an error for invalid JSON")
	}
}
