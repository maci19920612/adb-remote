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

var roomIdPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

// TestGenerateRoomIdFormat pins the room id to 12 Crockford base32
// characters (60 random bits): a room id is the only secret gating a room,
// so it must be far out of reach of guessing, not just unique.
func TestGenerateRoomIdFormat(t *testing.T) {
	roomId := GenerateRoomId()
	if !roomIdPattern.MatchString(roomId) {
		t.Fatalf("expected a room id matching %s, got %q", roomIdPattern, roomId)
	}
}

func TestGenerateRoomIdUsesTheWholeAlphabet(t *testing.T) {
	seen := make(map[rune]bool)
	for i := 0; i < 1000; i++ {
		for _, character := range GenerateRoomId() {
			seen[character] = true
		}
	}
	// 32 symbols plus the group separator.
	if len(seen) != len(roomIdAlphabet)+1 {
		t.Fatalf("expected every symbol of the alphabet to show up, saw %d distinct characters", len(seen))
	}
}
