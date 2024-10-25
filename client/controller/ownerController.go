package controller

import (
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"fmt"
	"github.com/mattn/go-tty"
)

func JoinAsRoomOwner(client *transportLayer.Client, deviceId string) {
	//logger := client.Logger
	mPool := client.TransporterMessagePool
	mChannel := client.MessageChannel

	err := client.SendCreateRoom()
	if err != nil {
		panic(err)
	}

	createRoomResponse := <-mChannel
	if createRoomResponse.IsError() {
		payload, err := createRoomResponse.GetErrorPayload()
		if err != nil {
			panic(err)
		} else {
			panic(fmt.Errorf("create room error: %x -- %s", payload.ErrorCode, payload.ErrorMessage))
		}
	}

	if err := protocol.ExpectCommand(createRoomResponse, protocol.CommandCreateRoom|protocol.CommandResponseMask); err != nil {
		panic(err)
	}

	payload, err := createRoomResponse.GetPayloadCreateRoomResponse()
	if err != nil {
		panic(err)
	}
	fmt.Printf("Your room id is: %s\n", payload.RoomId)
	mPool.Release(createRoomResponse)

	//Waiting for the incoming connection
	for {
		err := waitForRoomJoinRequest(client)
		if err != nil {
			fmt.Println("Error during the room join request execution: ", err)
			continue
		}

	}
}

func waitForRoomJoinRequest(client *transportLayer.Client) error {
	logger := client.Logger
	mPool := client.TransporterMessagePool
	mChannel := client.MessageChannel

	logger.Info("Waiting for room join request")
	joinRoomRequestMessage := <-mChannel
	defer mPool.Release(joinRoomRequestMessage)

	if err := protocol.ExpectCommand(joinRoomRequestMessage, protocol.CommandJoinRoom); err != nil {
		return err
	}

	joinRoomRequestPayload, err := joinRoomRequestMessage.GetPayloadConnectRoom()
	if err != nil {
		_ = client.SendError(protocol.CommandJoinRoom, protocol.ErrorInvalidPayload, "Invalid join room request payload")
		panic(err)
	}

	ttySession, err := tty.Open()
	defer ttySession.Close()
	if err != nil {
		_ = client.SendError(protocol.CommandJoinRoom, protocol.ErrorUnknown, "Client side error, connection will be closed")
		panic(err)
	}
	var isAccepted = 0
	for {
		fmt.Printf("Do you accept the room join request (clientId:%s) (y/n): ", joinRoomRequestPayload.ClientId)
		rawAnswer, err := ttySession.ReadRune()
		if err != nil {
			fmt.Println("Error during the TTY reading: ", err)
			continue
		}
		fmt.Printf(" %c \n", rawAnswer)
		if rawAnswer != 'y' && rawAnswer != 'n' {
			fmt.Println("Your answer is not acceptable, the only acceptable answers: y/n")
			continue
		}
		if rawAnswer == 'y' {
			isAccepted = 1
		}
		break
	}
	return client.SendJoinRoomResponse(isAccepted)
}
