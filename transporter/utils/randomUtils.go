package utils

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

const characterSegmentSize = 4
const numericSegmentSize = 4

// GenerateClientId returns a random client/room id. Room ids are the sole
// access-control secret gating a room (see roomManager.go): anyone who
// knows one can join it. That makes crypto/rand a correctness requirement
// here, not just hygiene — math/rand's generator is predictable from its
// own output, and any client can harvest plenty of that output simply by
// creating rooms.
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
