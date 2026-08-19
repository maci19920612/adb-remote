package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/relay"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"fmt"
	"github.com/mattn/go-tty"
)

// AcceptPromptFunc decides whether a room join request from guestClientId
// should be accepted.
type AcceptPromptFunc func(guestClientId string) (accepted bool, err error)

// JoinAsRoomOwner creates a room sharing deviceId and then, for every guest
// whose join request promptAccept approves, relays ADB protocol traffic
// between the local device (reached through smartSocket) and the guest
// until the room is vacated, looping to accept further guests until ctx is
// cancelled.
func JoinAsRoomOwner(ctx context.Context, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, deviceId string, promptAccept AcceptPromptFunc) error {
	logger := client.Logger

	roomId, err := createRoom(client)
	if err != nil {
		return err
	}
	fmt.Printf("Your room id is: %s\n", roomId)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		accepted, err := waitForRoomJoinRequest(client, promptAccept)
		if err != nil {
			fmt.Println("Error during the room join request handling: ", err)
			continue
		}
		if !accepted {
			continue
		}

		conn, err := smartSocket.Transport(deviceId)
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to open a transport to the local device %s: %s", deviceId, err))
			continue
		}
		logger.Info("Starting the relay with the guest")
		err = relay.Run(ctx, conn, client, logger)
		logger.Info(fmt.Sprintf("Relay stopped: %s", err))
	}
}

func createRoom(client *transportLayer.Client) (string, error) {
	if err := client.SendCreateRoom(); err != nil {
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
		return "", fmt.Errorf("create room error: %x -- %s", payload.ErrorCode, payload.ErrorMessage)
	}
	if err := protocol.ExpectCommand(message, protocol.CommandCreateRoom|protocol.CommandResponseMask); err != nil {
		return "", err
	}
	payload, err := message.GetPayloadCreateRoomResponse()
	if err != nil {
		return "", err
	}
	return payload.RoomId, nil
}

// waitForRoomJoinRequest blocks for the next join request, asks
// promptAccept whether to accept it, and reports the decision back to the
// transporter. The returned bool reports whether the request was accepted.
func waitForRoomJoinRequest(client *transportLayer.Client, promptAccept AcceptPromptFunc) (bool, error) {
	logger := client.Logger

	logger.Info("Waiting for room join request")
	container := <-client.Messages()
	defer container.Dispose()
	joinRoomRequestMessage, err := container.Data()
	if err != nil {
		return false, err
	}

	if err := protocol.ExpectCommand(joinRoomRequestMessage, protocol.CommandJoinRoom); err != nil {
		return false, err
	}

	joinRoomRequestPayload, err := joinRoomRequestMessage.GetPayloadConnectRoom()
	if err != nil {
		_ = client.SendError(protocol.CommandJoinRoom, protocol.ErrorInvalidPayload, "Invalid join room request payload")
		return false, err
	}

	accepted, err := promptAccept(joinRoomRequestPayload.ClientId)
	if err != nil {
		_ = client.SendError(protocol.CommandJoinRoom, protocol.ErrorUnknown, "Client side error, connection will be closed")
		return false, err
	}

	isAccepted := 0
	if accepted {
		isAccepted = 1
	}
	if err := client.SendJoinRoomResponse(isAccepted); err != nil {
		return false, err
	}
	return accepted, nil
}

// TTYAcceptPrompt asks the operator, over the controlling TTY, whether to
// accept a room join request.
func TTYAcceptPrompt(guestClientId string) (bool, error) {
	ttySession, err := tty.Open()
	if err != nil {
		return false, err
	}
	defer ttySession.Close()

	for {
		fmt.Printf("Do you accept the room join request (clientId:%s) (y/n): ", guestClientId)
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
		return rawAnswer == 'y', nil
	}
}
