package utils

import "testing"

func TestGenerateClientId(t *testing.T) {
	clientId := GenerateClientId()
	t.Log("ClientId: ", clientId)
}
