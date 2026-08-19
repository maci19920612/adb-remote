package utils

import (
	"regexp"
	"testing"
)

var clientIdPattern = regexp.MustCompile(`^[A-Z]{4}[0-9]{4}$`)

func TestGenerateClientIdFormat(t *testing.T) {
	clientId := GenerateClientId()
	if !clientIdPattern.MatchString(clientId) {
		t.Fatalf("expected a client id matching %s, got %q", clientIdPattern, clientId)
	}
}

func TestGenerateClientIdIsReasonablyUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := GenerateClientId()
		if seen[id] {
			t.Fatalf("generated a duplicate client id after %d iterations: %s", i, id)
		}
		seen[id] = true
	}
}
