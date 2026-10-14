package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// DefaultTLSCertFile and DefaultTLSKeyFile are used when
// TransporterConfiguration.TLSCertFile/TLSKeyFile are left empty. The
// transporter generates a self-signed certificate/key pair there on first
// run if neither file exists yet (see transporter/tlsutil).
const (
	DefaultTLSCertFile = "transporter-cert.pem"
	DefaultTLSKeyFile  = "transporter-key.pem"
)

// VerbosityDefault and VerbosityDebug are the accepted
// TransporterConfiguration.Verbosity values; an empty Verbosity means
// VerbosityDefault. VerbosityDebug turns on debug logging and records every
// byte of every connection to PcapPath (see transporter/capture).
const (
	VerbosityDefault = "default"
	VerbosityDebug   = "debug"
)

// DefaultPcapFile is used when TransporterConfiguration.PcapFile is left
// empty.
const DefaultPcapFile = "transporter.pcapng"

type TransporterConfiguration struct {
	Address     string `json:"transporterAddress"`
	TLSCertFile string `json:"tlsCertFile,omitempty"`
	TLSKeyFile  string `json:"tlsKeyFile,omitempty"`
	Verbosity   string `json:"verbosity,omitempty"`
	PcapFile    string `json:"pcapFile,omitempty"`
}

// CertPath returns the configured TLS certificate path, or
// DefaultTLSCertFile if unset.
func (c *TransporterConfiguration) CertPath() string {
	if c.TLSCertFile != "" {
		return c.TLSCertFile
	}
	return DefaultTLSCertFile
}

// KeyPath returns the configured TLS key path, or DefaultTLSKeyFile if
// unset.
func (c *TransporterConfiguration) KeyPath() string {
	if c.TLSKeyFile != "" {
		return c.TLSKeyFile
	}
	return DefaultTLSKeyFile
}

// IsDebug reports whether debug verbosity (debug logging plus packet
// capture) is enabled.
func (c *TransporterConfiguration) IsDebug() bool {
	return c.Verbosity == VerbosityDebug
}

// PcapPath returns the configured packet capture path, or DefaultPcapFile
// if unset.
func (c *TransporterConfiguration) PcapPath() string {
	if c.PcapFile != "" {
		return c.PcapFile
	}
	return DefaultPcapFile
}

func CreateConfig(path string) (*TransporterConfiguration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config TransporterConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	switch config.Verbosity {
	case "", VerbosityDefault, VerbosityDebug:
	default:
		// Rejected rather than treated as VerbosityDefault, so a typo
		// doesn't silently leave packet capture off.
		return nil, fmt.Errorf("invalid verbosity %q, expected %q or %q", config.Verbosity, VerbosityDefault, VerbosityDebug)
	}
	return &config, nil
}
