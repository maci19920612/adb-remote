package controller

import (
	"adb-remote.maci.team/client/transportLayer"
	"fmt"
)

func Handshake(client *transportLayer.Client) {
	logger := client.Logger
	mPool := client.TransporterMessagePool

	logger.Info("Handshake started")
	err := client.SendConnect()
	if err != nil {
		panic(err)
	}
	message := <-client.MessageChannel
	defer mPool.Release(message)
	if message.IsError() {
		payload, err := message.GetErrorPayload()
		if err != nil {
			panic(err)
		} else {
			panic(fmt.Errorf("connect error: %x -- %s", payload.ErrorCode, payload.ErrorMessage))
		}
	}
	payload, err := message.GetPayloadConnectResponse()
	if err != nil {
		panic(err)
	}
	fmt.Printf("You have to transfer your client ID in a separate channel to the room owne\nr")
	fmt.Printf("Your client id: %s", payload.ClientId)

}
