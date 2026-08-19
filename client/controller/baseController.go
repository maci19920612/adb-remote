package controller

import (
	"adb-remote.maci.team/client/transportLayer"
	"fmt"
)

// Handshake performs the initial protocol handshake with the transporter
// and prints the client id assigned to this session.
func Handshake(client *transportLayer.Client) error {
	logger := client.Logger

	logger.Info("Handshake started")
	if err := client.SendConnect(); err != nil {
		return err
	}
	container := <-client.Messages()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
	if message.IsError() {
		payload, err := message.GetErrorPayload()
		if err != nil {
			return err
		}
		return fmt.Errorf("connect error: %x -- %s", payload.ErrorCode, payload.ErrorMessage)
	}
	payload, err := message.GetPayloadConnectResponse()
	if err != nil {
		return err
	}
	fmt.Println("You have to transfer your client ID in a separate channel to the room owner")
	fmt.Printf("Your client id: %s\n", payload.ClientId)
	return nil
}
