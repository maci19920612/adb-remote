package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/identity"
	"adb-remote.maci.team/client/relay"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"fmt"
)

// AcceptPromptFunc decides whether a room join request from guestClientId
// should be accepted. guestPublicKey is the guest's identity public key (see
// client/identity); the caller should display its fingerprint so the
// operator can verify it out of band before accepting.
type AcceptPromptFunc func(guestClientId string, guestPublicKey []byte) (accepted bool, err error)

// JoinAsRoomOwner creates a room sharing deviceId, then services the room
// for its whole lifetime: every ADB stream a guest opens is relayed via a
// relay.OwnerMultiplexer, and every join request is handed to promptAccept
// off the dispatch loop (promptAccept commonly blocks on user input; it
// must not stall ADB traffic for a guest that is already connected). State
// changes are reported through onEvent; all presentation is the caller's
// responsibility. Returns when ctx is cancelled or the transporter
// connection is lost.
func JoinAsRoomOwner(ctx context.Context, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, deviceId string, ownerIdentity *identity.Identity, promptAccept AcceptPromptFunc, onEvent OwnerEventFunc) error {
	logger := client.Logger

	roomId, err := createRoom(client)
	if err != nil {
		return err
	}
	emitOwner(onEvent, OwnerEvent{Kind: OwnerRoomCreated, RoomId: roomId})

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
			dispatchOwnerMessage(client, multiplexer, ownerIdentity, promptAccept, onEvent, container)
		}
	}
}

// dispatchOwnerMessage routes a single incoming message either to the ADB
// stream multiplexer or to the room join-request flow.
func dispatchOwnerMessage(client *transportLayer.Client, multiplexer *relay.OwnerMultiplexer, ownerIdentity *identity.Identity, promptAccept AcceptPromptFunc, onEvent OwnerEventFunc, container *transportLayer.MessageContainer) {
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
		emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinRequested, GuestClientId: payload.ClientId, GuestPublicKey: payload.PublicKey})
		// promptAccept commonly blocks on user input; run it off the
		// dispatch loop so an already-connected guest's ADB traffic keeps
		// flowing while the operator decides.
		go handleJoinRequest(client, ownerIdentity, promptAccept, onEvent, payload.ClientId, payload.PublicKey)
	case protocol.CommandGuestLeft:
		defer container.Dispose()
		logger.Info("The guest left the room")
		// Only one guest is ever active at a time, so every currently open
		// stream necessarily belonged to it.
		multiplexer.Close()
		emitOwner(onEvent, OwnerEvent{Kind: OwnerGuestLeft})
	default:
		defer container.Dispose()
		logger.Info(fmt.Sprintf("Ignoring unexpected message, command: %x", message.Command()))
	}
}

func handleJoinRequest(client *transportLayer.Client, ownerIdentity *identity.Identity, promptAccept AcceptPromptFunc, onEvent OwnerEventFunc, guestClientId string, guestPublicKey []byte) {
	logger := client.Logger

	accepted, err := promptAccept(guestClientId, guestPublicKey)
	if err != nil {
		logger.Error(fmt.Sprintf("Error while deciding whether to accept the join request from %s: %s", guestClientId, err))
		emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinFailed, GuestClientId: guestClientId, Err: err})
		if err := client.SendError(protocol.CommandJoinRoom, protocol.ErrorUnknown, "Client side error, connection will be closed"); err != nil {
			logger.Error(fmt.Sprintf("Failed to send the join-request error: %s", err))
		}
		return
	}

	isAccepted := 0
	if accepted {
		isAccepted = 1
	}
	if err := client.SendJoinRoomResponse(isAccepted, ownerIdentity.PublicKey); err != nil {
		logger.Error(fmt.Sprintf("Failed to send the join room response for %s: %s", guestClientId, err))
		emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinFailed, GuestClientId: guestClientId, Err: err})
		return
	}
	emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinDecided, GuestClientId: guestClientId, GuestPublicKey: guestPublicKey, Accepted: accepted})
}

func createRoom(client *transportLayer.Client) (string, error) {
	if err := client.SendCreateRoom(); err != nil {
		return "", err
	}

	container, ok := <-client.Messages()
	if !ok {
		return "", relay.ErrTransportClosed
	}
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
