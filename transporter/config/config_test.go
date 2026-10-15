package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestLimitsDefaultWhenOmitted(t *testing.T) {
	config, err := CreateConfig(writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000"}`))
	if err != nil {
		t.Fatalf("CreateConfig failed: %s", err)
	}
	if config.ConnectionLimit() != DefaultMaxConnections || config.ConnectionLimitPerIP() != DefaultMaxConnectionsPerIP {
		t.Fatalf("expected the default connection limits, got %d/%d", config.ConnectionLimit(), config.ConnectionLimitPerIP())
	}
	if config.RoomLimit() != DefaultMaxRooms || config.RoomLimitPerIP() != DefaultMaxRoomsPerIP {
		t.Fatalf("expected the default room limits, got %d/%d", config.RoomLimit(), config.RoomLimitPerIP())
	}
	if config.RoomIdleTimeoutDuration() != DefaultRoomIdleTimeout {
		t.Fatalf("expected the default room idle timeout, got %s", config.RoomIdleTimeoutDuration())
	}
}

func TestCreateConfigParsesLimits(t *testing.T) {
	config, err := CreateConfig(writeConfigFile(t, `{
		"transporterAddress": "0.0.0.0:9000",
		"maxConnections": 10,
		"maxConnectionsPerIp": 2,
		"maxRooms": 5,
		"maxRoomsPerIp": 1,
		"roomIdleTimeout": "90m"
	}`))
	if err != nil {
		t.Fatalf("CreateConfig failed: %s", err)
	}
	if config.ConnectionLimit() != 10 || config.ConnectionLimitPerIP() != 2 || config.RoomLimit() != 5 || config.RoomLimitPerIP() != 1 {
		t.Fatalf("expected the configured limits, got %+v", config)
	}
	if config.RoomIdleTimeoutDuration() != 90*time.Minute {
		t.Fatalf("expected a 90m room idle timeout, got %s", config.RoomIdleTimeoutDuration())
	}
}

func TestRoomIdleTimeoutZeroDisablesIt(t *testing.T) {
	config, err := CreateConfig(writeConfigFile(t, `{"transporterAddress": "0.0.0.0:9000", "roomIdleTimeout": "0"}`))
	if err != nil {
		t.Fatalf("CreateConfig failed: %s", err)
	}
	if config.RoomIdleTimeoutDuration() != 0 {
		t.Fatalf("expected the room idle timeout to be disabled, got %s", config.RoomIdleTimeoutDuration())
	}
}

func TestCreateConfigRejectsInvalidLimits(t *testing.T) {
	for _, content := range []string{
		`{"transporterAddress": "0.0.0.0:9000", "maxConnections": -1}`,
		`{"transporterAddress": "0.0.0.0:9000", "maxConnectionsPerIp": -1}`,
		`{"transporterAddress": "0.0.0.0:9000", "maxRooms": -1}`,
		`{"transporterAddress": "0.0.0.0:9000", "maxRoomsPerIp": -1}`,
		`{"transporterAddress": "0.0.0.0:9000", "roomIdleTimeout": "soon"}`,
		`{"transporterAddress": "0.0.0.0:9000", "roomIdleTimeout": "-5m"}`,
	} {
		if _, err := CreateConfig(writeConfigFile(t, content)); err == nil {
			t.Fatalf("expected an error for %s", content)
		}
	}
}
