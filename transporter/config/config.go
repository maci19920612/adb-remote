package config

import (
	"encoding/json"
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

type TransporterConfiguration struct {
	Address     string `json:"transporterAddress"`
	TLSCertFile string `json:"tlsCertFile,omitempty"`
	TLSKeyFile  string `json:"tlsKeyFile,omitempty"`
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

func CreateConfig(path string) (*TransporterConfiguration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config TransporterConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}
