package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
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

// Defaults for the resource limits, used when the corresponding
// TransporterConfiguration field is left at zero. They bound how much of
// the transporter a single client address (or everyone together) can tie
// up with connections and rooms.
const (
	DefaultMaxConnections      = 1024
	DefaultMaxConnectionsPerIP = 32
	DefaultMaxRooms            = 512
	DefaultMaxRoomsPerIP       = 8
	DefaultRoomIdleTimeout     = 24 * time.Hour
)

type TransporterConfiguration struct {
	Address     string `json:"transporterAddress"`
	TLSCertFile string `json:"tlsCertFile,omitempty"`
	TLSKeyFile  string `json:"tlsKeyFile,omitempty"`
	Verbosity   string `json:"verbosity,omitempty"`
	PcapFile    string `json:"pcapFile,omitempty"`

	MaxConnections      int `json:"maxConnections,omitempty"`
	MaxConnectionsPerIP int `json:"maxConnectionsPerIp,omitempty"`
	MaxRooms            int `json:"maxRooms,omitempty"`
	MaxRoomsPerIP       int `json:"maxRoomsPerIp,omitempty"`
	// RoomIdleTimeout is a Go duration ("90m", "24h"): a room that goes
	// this long without an accepted guest is closed. "0" disables it.
	RoomIdleTimeout string `json:"roomIdleTimeout,omitempty"`
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

// ConnectionLimit returns the maximum number of concurrent client
// connections, or DefaultMaxConnections if unset.
func (c *TransporterConfiguration) ConnectionLimit() int {
	return limitOrDefault(c.MaxConnections, DefaultMaxConnections)
}

// ConnectionLimitPerIP returns the maximum number of concurrent client
// connections from one address, or DefaultMaxConnectionsPerIP if unset.
func (c *TransporterConfiguration) ConnectionLimitPerIP() int {
	return limitOrDefault(c.MaxConnectionsPerIP, DefaultMaxConnectionsPerIP)
}

// RoomLimit returns the maximum number of rooms, or DefaultMaxRooms if
// unset.
func (c *TransporterConfiguration) RoomLimit() int {
	return limitOrDefault(c.MaxRooms, DefaultMaxRooms)
}

// RoomLimitPerIP returns the maximum number of rooms owned from one
// address, or DefaultMaxRoomsPerIP if unset.
func (c *TransporterConfiguration) RoomLimitPerIP() int {
	return limitOrDefault(c.MaxRoomsPerIP, DefaultMaxRoomsPerIP)
}

// RoomIdleTimeoutDuration returns how long a room may go without an
// accepted guest before it is closed: DefaultRoomIdleTimeout if unset, 0 if
// disabled. CreateConfig has already rejected a value that doesn't parse.
func (c *TransporterConfiguration) RoomIdleTimeoutDuration() time.Duration {
	if c.RoomIdleTimeout == "" {
		return DefaultRoomIdleTimeout
	}
	timeout, err := time.ParseDuration(c.RoomIdleTimeout)
	if err != nil {
		return DefaultRoomIdleTimeout
	}
	return timeout
}

func limitOrDefault(limit int, defaultLimit int) int {
	if limit > 0 {
		return limit
	}
	return defaultLimit
}

func (c *TransporterConfiguration) validate() error {
	switch c.Verbosity {
	case "", VerbosityDefault, VerbosityDebug:
	default:
		// Rejected rather than treated as VerbosityDefault, so a typo
		// doesn't silently leave packet capture off.
		return fmt.Errorf("invalid verbosity %q, expected %q or %q", c.Verbosity, VerbosityDefault, VerbosityDebug)
	}
	if c.MaxConnections < 0 || c.MaxConnectionsPerIP < 0 || c.MaxRooms < 0 || c.MaxRoomsPerIP < 0 {
		return errors.New("connection and room limits must not be negative")
	}
	if c.RoomIdleTimeout != "" {
		timeout, err := time.ParseDuration(c.RoomIdleTimeout)
		if err != nil {
			return fmt.Errorf("invalid roomIdleTimeout %q: %w", c.RoomIdleTimeout, err)
		}
		if timeout < 0 {
			return fmt.Errorf("invalid roomIdleTimeout %q: must not be negative", c.RoomIdleTimeout)
		}
	}
	return nil
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
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &config, nil
}
