package utils

import (
	"math/rand"
	"strings"
)

const characterSegmentSize = 4
const numericSegmentSize = 4

func GenerateClientId() string {
	clientIdBuilder := strings.Builder{}
	for i := 0; i < characterSegmentSize; i++ {
		clientIdBuilder.WriteRune(rune(rand.Int()%26 + int('A')))
	}
	for i := 0; i < numericSegmentSize; i++ {
		clientIdBuilder.WriteRune(rune(rand.Int()%10 + int('0')))
	}
	return clientIdBuilder.String()
}
