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

func TestCertKeyPathDefaults(t *testing.T) {
	config := &TransporterConfiguration{Address: "0.0.0.0:9000"}
	if config.CertPath() != DefaultTLSCertFile {
		t.Fatalf("expected the default cert path %q, got %q", DefaultTLSCertFile, config.CertPath())
	}
	if config.KeyPath() != DefaultTLSKeyFile {
		t.Fatalf("expected the default key path %q, got %q", DefaultTLSKeyFile, config.KeyPath())
	}
}

func TestCertKeyPathOverrides(t *testing.T) {
	config := &TransporterConfiguration{
		Address:     "0.0.0.0:9000",
		TLSCertFile: "custom-cert.pem",
		TLSKeyFile:  "custom-key.pem",
	}
	if config.CertPath() != "custom-cert.pem" {
		t.Fatalf("expected the overridden cert path, got %q", config.CertPath())
	}
	if config.KeyPath() != "custom-key.pem" {
		t.Fatalf("expected the overridden key path, got %q", config.KeyPath())
	}
}

func TestCreateConfigParsesTLSPaths(t *testing.T) {
	path := writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000", "tlsCertFile": "a.pem", "tlsKeyFile": "b.pem"}`)
	config, err := CreateConfig(path)
	if err != nil {
		t.Fatalf("CreateConfig failed: %s", err)
	}
	if config.CertPath() != "a.pem" || config.KeyPath() != "b.pem" {
		t.Fatalf("expected TLS paths a.pem/b.pem, got %q/%q", config.CertPath(), config.KeyPath())
	}
}

func TestCreateConfigVerbosityDefaultsWhenOmitted(t *testing.T) {
	path := writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000"}`)
	config, err := CreateConfig(path)
	if err != nil {
		t.Fatalf("CreateConfig failed: %s", err)
	}
	if config.IsDebug() {
		t.Fatalf("expected debug verbosity to be off when omitted")
	}
}

func TestCreateConfigParsesVerbosity(t *testing.T) {
	for _, testCase := range []struct {
		verbosity string
		debug     bool
	}{
		{VerbosityDefault, false},
		{VerbosityDebug, true},
	} {
		path := writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000", "verbosity": "`+testCase.verbosity+`"}`)
		config, err := CreateConfig(path)
		if err != nil {
			t.Fatalf("CreateConfig failed for verbosity %q: %s", testCase.verbosity, err)
		}
		if config.IsDebug() != testCase.debug {
			t.Fatalf("expected IsDebug()=%t for verbosity %q", testCase.debug, testCase.verbosity)
		}
	}
}

func TestCreateConfigRejectsUnknownVerbosity(t *testing.T) {
	path := writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000", "verbosity": "Debug"}`)
	if _, err := CreateConfig(path); err == nil {
		t.Fatalf("expected an error for an unknown verbosity value")
	}
}

func TestPcapPathDefault(t *testing.T) {
	config := &TransporterConfiguration{Address: "0.0.0.0:9000"}
	if config.PcapPath() != DefaultPcapFile {
		t.Fatalf("expected the default pcap path %q, got %q", DefaultPcapFile, config.PcapPath())
	}
}

func TestCreateConfigParsesPcapFile(t *testing.T) {
	path := writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000", "pcapFile": "/captures/t.pcapng"}`)
	config, err := CreateConfig(path)
	if err != nil {
		t.Fatalf("CreateConfig failed: %s", err)
	}
	if config.PcapPath() != "/captures/t.pcapng" {
		t.Fatalf("expected the overridden pcap path, got %q", config.PcapPath())
	}
}
