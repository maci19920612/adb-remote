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

// JoinAsRoomOwner creates a room sharing deviceId, then services the room
// for its whole lifetime: every ADB stream a guest opens is relayed via an
// relay.OwnerMultiplexer, and every join request is handed to promptAccept
// off the dispatch loop (promptAccept commonly blocks on TTY input; it must
// not stall ADB traffic for a guest that is already connected). Returns
// when ctx is cancelled or the transporter connection is lost.
func JoinAsRoomOwner(ctx context.Context, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, deviceId string, promptAccept AcceptPromptFunc) error {
	logger := client.Logger

	roomId, err := createRoom(client)
	if err != nil {
		return err
	}
	fmt.Printf("Your room id is: %s\n", roomId)

	multiplexer := relay.NewOwnerMultiplexer(smartSocket, deviceId, client, logger)
	defer multiplexer.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case container, ok := <-client.Messages():
			if !ok {
				return relay.ErrTransportClosed
			}
			dispatchOwnerMessage(client, multiplexer, promptAccept, container)
		}
	}
}

// dispatchOwnerMessage routes a single incoming message either to the ADB
// stream multiplexer or to the room join-request flow.
func dispatchOwnerMessage(client *transportLayer.Client, multiplexer *relay.OwnerMultiplexer, promptAccept AcceptPromptFunc, container *transportLayer.MessageContainer) {
	logger := client.Logger

	message, err := container.Data()
	if err != nil {
		_ = container.Dispose()
		logger.Error(fmt.Sprintf("Unusable pooled message container: %s", err))
		return
	}

	switch message.Command() {
	case protocol.CommandAdbTransport:
		multiplexer.Dispatch(container) // disposes container itself
	case protocol.CommandJoinRoom:
		defer container.Dispose()
		payload, err := message.GetPayloadConnectRoom()
		if err != nil {
			logger.Error(fmt.Sprintf("Invalid join room request payload: %s", err))
			if err := client.SendError(protocol.CommandJoinRoom, protocol.ErrorInvalidPayload, "Invalid join room request payload"); err != nil {
				logger.Error(fmt.Sprintf("Failed to send the invalid-payload error: %s", err))
			}
			return
		}
		// promptAccept commonly blocks on TTY input; run it off the
		// dispatch loop so an already-connected guest's ADB traffic keeps
		// flowing while the operator decides.
		go handleJoinRequest(client, promptAccept, payload.ClientId)
	default:
		defer container.Dispose()
		logger.Info(fmt.Sprintf("Ignoring unexpected message, command: %x", message.Command()))
	}
}

func handleJoinRequest(client *transportLayer.Client, promptAccept AcceptPromptFunc, guestClientId string) {
	logger := client.Logger

	accepted, err := promptAccept(guestClientId)
	if err != nil {
		logger.Error(fmt.Sprintf("Error while deciding whether to accept the join request from %s: %s", guestClientId, err))
		if err := client.SendError(protocol.CommandJoinRoom, protocol.ErrorUnknown, "Client side error, connection will be closed"); err != nil {
			logger.Error(fmt.Sprintf("Failed to send the join-request error: %s", err))
		}
		return
	}

	isAccepted := 0
	if accepted {
		isAccepted = 1
	}
	if err := client.SendJoinRoomResponse(isAccepted); err != nil {
		logger.Error(fmt.Sprintf("Failed to send the join room response for %s: %s", guestClientId, err))
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
