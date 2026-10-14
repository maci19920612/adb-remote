package di

import (
	"adb-remote.maci.team/transporter/config"
	"context"
	"log/slog"
	"testing"
)

func TestNewLoggerEnablesDebugOnlyForDebugVerbosity(t *testing.T) {
	for _, testCase := range []struct {
		verbosity string
		debug     bool
	}{
		{"", false},
		{config.VerbosityDefault, false},
		{config.VerbosityDebug, true},
	} {
		logger := newLogger(&config.TransporterConfiguration{Verbosity: testCase.verbosity})
		if logger.Enabled(context.Background(), slog.LevelDebug) != testCase.debug {
			t.Fatalf("expected debug logging enabled=%t for verbosity %q", testCase.debug, testCase.verbosity)
		}
		if !logger.Enabled(context.Background(), slog.LevelInfo) {
			t.Fatalf("expected info logging to stay enabled for verbosity %q", testCase.verbosity)
		}
	}
}
