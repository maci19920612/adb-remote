package controller

import (
	"adb-remote.maci.team/client/transportLayer"
	"fmt"
)

// Handshake performs the initial protocol handshake with the transporter
// and returns the client id assigned to this session. Presentation (e.g.
// telling the user to share it with the room owner) is the caller's
// responsibility.
func Handshake(client *transportLayer.Client) (string, error) {
	logger := client.Logger

	logger.Info("Handshake started")
	if err := client.SendConnect(); err != nil {
		return "", err
	}
	container := <-client.Messages()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return "", err
	}
	if message.IsError() {
		payload, err := message.GetErrorPayload()
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("connect error: %x -- %s", payload.ErrorCode, payload.ErrorMessage)
	}
	payload, err := message.GetPayloadConnectResponse()
	if err != nil {
		return "", err
	}
	return payload.ClientId, nil
}
