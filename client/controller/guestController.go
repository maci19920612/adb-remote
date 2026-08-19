package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/relay"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"fmt"
)

type ErrJoinRoomDenied struct {
	RoomId string
}

func (e *ErrJoinRoomDenied) Error() string {
	return fmt.Sprintf("join room request denied: %s", e.RoomId)
}

// JoinAsGuest joins roomId as a guest, then starts a local AdbProxy on
// localPort and relays ADB protocol traffic between it and the room owner
// until ctx is cancelled or the proxy fails to start.
func JoinAsGuest(ctx context.Context, client *transportLayer.Client, roomId string, localPort string) error {
	if err := roomJoinStep(client, roomId); err != nil {
		return err
	}

	logger := client.Logger
	proxy := adb.NewAdbProxy(localPort, logger)
	if err := proxy.Start(roomId); err != nil {
		return err
	}
	defer proxy.Stop()

	fmt.Printf("Connected. Point your local ADB at this device with: adb connect 127.0.0.1:%s\n", localPort)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case conn := <-proxy.Connections():
			logger.Info("Local ADB server connected, starting the relay")
			err := relay.Run(ctx, conn, client, logger)
			logger.Info(fmt.Sprintf("Relay stopped: %s", err))
		}
	}
}

func roomJoinStep(client *transportLayer.Client, roomId string) error {
	logger := client.Logger
	logger.Info(fmt.Sprintf("Joining room %s", roomId))
	if err := client.SendJoinRoom(roomId); err != nil {
		logger.Error(fmt.Sprintf("Failed to join room: %s, error: %s", roomId, err))
		return err
	}
	container := <-client.Messages()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
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
		return &ErrJoinRoomDenied{RoomId: roomId}
	}
	logger.Info(fmt.Sprintf("Joined room: %s", roomId))
	return nil
}
