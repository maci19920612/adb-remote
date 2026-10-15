package utils

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

const characterSegmentSize = 4
const numericSegmentSize = 4

// roomIdAlphabet is Crockford's base32 alphabet: digits and upper case
// letters without I, L, O and U, so a room id read out loud or retyped
// can't be confused.
const roomIdAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const roomIdGroups = 3
const roomIdGroupSize = 4

// GenerateRoomId returns a random room id: 12 base32 symbols (60 bits) in
// groups of four, e.g. "7KQ2-M9XD-4HTB". A room id is the sole secret
// gating a room (see roomManager.go): anyone who knows one can ask to join
// it, so it has to be out of reach of guessing, not just unique. That also
// makes crypto/rand a correctness requirement here, not just hygiene —
// math/rand's generator is predictable from its own output, and any client
// can harvest plenty of that output simply by creating rooms.
func GenerateRoomId() string {
	roomIdBuilder := strings.Builder{}
	for group := 0; group < roomIdGroups; group++ {
		if group > 0 {
			roomIdBuilder.WriteByte('-')
		}
		for i := 0; i < roomIdGroupSize; i++ {
			roomIdBuilder.WriteByte(roomIdAlphabet[randomIntn(int64(len(roomIdAlphabet)))])
		}
	}
	return roomIdBuilder.String()
}

// GenerateClientId returns a random client id. Client ids only label a
// connection (they're shown to people, not used to grant anything), so
// they stay short.
func GenerateClientId() string {
	clientIdBuilder := strings.Builder{}
	for i := 0; i < characterSegmentSize; i++ {
		clientIdBuilder.WriteRune(rune(randomIntn(26) + int('A')))
	}
	for i := 0; i < numericSegmentSize; i++ {
		clientIdBuilder.WriteRune(rune(randomIntn(10) + int('0')))
	}
	return clientIdBuilder.String()
}

// randomIntn returns a cryptographically random integer in [0, n). It
// panics if the OS entropy source itself fails, which in practice never
// happens on any supported platform and would indicate a broken system —
// there is no sensible fallback that wouldn't undermine the whole point of
// using crypto/rand here.
func randomIntn(n int64) int {
	value, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %s", err))
	}
	return int(value.Int64())
}
