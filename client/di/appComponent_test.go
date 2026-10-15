package di

import (
	"adb-remote.maci.team/client/command"
	"os"
	"strings"
	"testing"
)

// TestInvalidConfigIsReportedAsAnError checks a mistake in config.json,
// such as a mistyped pinned certificate fingerprint, reaches the user as
// an error message instead of a panic with a stack trace.
func TestInvalidConfigIsReportedAsAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	config := `{"transporterAddress": "127.0.0.1:9000", "transporterCertificateFingerprint": "SHA256:typo"}`
	if err := os.WriteFile("config.json", []byte(config), 0o600); err != nil {
		t.Fatalf("failed to write the config file: %s", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected an error, got a panic: %v", r)
		}
	}()

	var commands []*command.Command[command.BaseCommand]
	err := CreateContainer().Resolve(&commands)
	if err == nil || !strings.Contains(err.Error(), "transporterCertificateFingerprint") {
		t.Fatalf("expected an error naming the invalid setting, got %v", err)
	}
}
