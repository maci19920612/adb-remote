package controller

import (
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"fmt"
)

type ErrJoinRoomDenied struct {
	RoomId string
}

func (e *ErrJoinRoomDenied) Error() string {
	return fmt.Sprintf("Join room request denied: %s", e.RoomId)
}

func JoinAsGuest(client *transportLayer.Client, roomId string) error {
	/**
	Steps:
	- Connect to the remote room
	- Start the smart socket server
	- Connect the local ADB instance to that server
	*/
	var err error
	err = roomJoinStep(client, roomId)
	if err != nil {
		return err
	}
}

func roomJoinStep(client *transportLayer.Client, roomId string) error {
	var err error
	logger := client.Logger
	mPool := client.TransporterMessagePool
	logger.Info(fmt.Sprintf("Joining room %s", roomId))
	err = client.SendJoinRoom(roomId)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to join room: %s, error: %s", roomId, err))
		return err
	}
	message := <-client.MessageChannel
	defer mPool.Release(message)
	if err := protocol.ExpectCommand(message, protocol.CommandJoinRoom|protocol.CommandResponseMask); err != nil {
		logger.Error(fmt.Sprintf("Unexpected message (expected: JoinRoomResponse): %x", message.Command()))
		return err
	}
	payload, err := message.GetPayloadConnectRoomResponse()
	if err != nil {
		logger.Error(fmt.Sprintf("Invalid join room response payload: %s", err))
		return err
	}
	if payload.Accepted == 0 {
		logger.Error(fmt.Sprintf("Join room declined, roomId: %s", roomId))
		return &ErrJoinRoomDenied{roomId}
	}
	logger.Info(fmt.Sprintf("Joined to room: %s", roomId))
	return nil
}

func startSmartAdbSocket() {
	
}
