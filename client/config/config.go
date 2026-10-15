package config

import (
	"adb-remote.maci.team/shared/certfingerprint"
	"encoding/json"
	"fmt"
	"os"
)

const DefaultConfigPath = "./config.json"

type ClientConfiguration struct {
	TransporterAddress string `json:"transporterAddress"`
	// TransporterCertificateFingerprint optionally pins the transporter's
	// TLS certificate: when set, the client only talks to a transporter
	// presenting the certificate with this fingerprint ("SHA256:...", as
	// the transporter prints it on startup).
	TransporterCertificateFingerprint string `json:"transporterCertificateFingerprint,omitempty"`
}

func CreateConfig() (*ClientConfiguration, error) {
	return LoadConfig(DefaultConfigPath)
}

func LoadConfig(path string) (*ClientConfiguration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	config := ClientConfiguration{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	if config.TransporterCertificateFingerprint != "" {
		// Rejected rather than ignored, so a typo doesn't silently turn
		// pinning off.
		if _, err := certfingerprint.Parse(config.TransporterCertificateFingerprint); err != nil {
			return nil, fmt.Errorf("invalid transporterCertificateFingerprint: %w", err)
		}
	}
	return &config, nil
}
