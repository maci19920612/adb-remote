package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/e2e"
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
// responsibility. Returns when ctx is cancelled, the transporter closes
// the room or the transporter connection is lost.
//
// ADB traffic only flows with a guest the owner accepted: accepting
// installs the end-to-end session with that guest (see client/e2e), and
// the client drops ADB traffic arriving without one.
func JoinAsRoomOwner(ctx context.Context, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, deviceId string, ownerIdentity *identity.Identity, promptAccept AcceptPromptFunc, onEvent OwnerEventFunc) error {
	logger := client.Logger

	roomId, err := createRoom(client)
	if err != nil {
		return err
	}
	emitOwner(onEvent, OwnerEvent{Kind: OwnerRoomCreated, RoomId: roomId})

	multiplexer := relay.NewOwnerMultiplexer(smartSocket, deviceId, client, logger)
	defer multiplexer.Close()
	defer client.SetSession(nil)

	room := &ownerRoom{
		client:        client,
		multiplexer:   multiplexer,
		roomId:        roomId,
		ownerIdentity: ownerIdentity,
		promptAccept:  promptAccept,
		onEvent:       onEvent,
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case container, ok := <-client.Messages():
			if !ok {
				return relay.ErrTransportClosed
			}
			if err := room.dispatch(container); err != nil {
				return err
			}
		}
	}
}

// ownerRoom is what the owner's dispatch loop needs to serve its room.
type ownerRoom struct {
	client        *transportLayer.Client
	multiplexer   *relay.OwnerMultiplexer
	roomId        string
	ownerIdentity *identity.Identity
	promptAccept  AcceptPromptFunc
	onEvent       OwnerEventFunc
}

// dispatch routes a single incoming message either to the ADB stream
// multiplexer or to the room join-request flow. It returns an error once
// the room is over.
func (r *ownerRoom) dispatch(container *transportLayer.MessageContainer) error {
	logger := r.client.Logger

	message, err := container.Data()
	if err != nil {
		_ = container.Dispose()
		logger.Error(fmt.Sprintf("Unusable pooled message container: %s", err))
		return nil
	}

	switch message.Command() {
	case protocol.CommandAdbTransport:
		// Only delivered within a session, i.e. from an accepted guest.
		r.multiplexer.Dispatch(container) // disposes container itself
	case protocol.CommandJoinRoom:
		defer container.Dispose()
		payload, err := message.GetPayloadConnectRoom()
		if err != nil {
			logger.Error(fmt.Sprintf("Invalid join room request payload: %s", err))
			if err := r.client.SendError(protocol.CommandJoinRoom, protocol.ErrorInvalidPayload, "Invalid join room request payload"); err != nil {
				logger.Error(fmt.Sprintf("Failed to send the invalid-payload error: %s", err))
			}
			return nil
		}
		r.handleJoinRequestPayload(payload)
	case protocol.CommandGuestLeft:
		defer container.Dispose()
		logger.Info("The guest left the room")
		r.client.SetSession(nil)
		// Only one guest is ever active at a time, so every currently open
		// stream necessarily belonged to it.
		r.multiplexer.Close()
		emitOwner(r.onEvent, OwnerEvent{Kind: OwnerGuestLeft})
	case protocol.CommandCreateRoom | protocol.CommandErrorResponseMask:
		defer container.Dispose()
		reason := "unknown reason"
		if payload, err := message.GetErrorPayload(); err == nil {
			reason = payload.ErrorMessage
		}
		return fmt.Errorf("the transporter closed the room: %s", reason)
	default:
		defer container.Dispose()
		if message.IsError() {
			if payload, err := message.GetErrorPayload(); err == nil {
				logger.Warn(fmt.Sprintf("The transporter refused a request (command %x): %s", message.Command(), payload.ErrorMessage))
				return nil
			}
		}
		logger.Info(fmt.Sprintf("Ignoring unexpected message, command: %x", message.Command()))
	}
	return nil
}

// handleJoinRequestPayload checks a join request carries a key exchange
// signed by the identity it presents before anyone is asked about it: one
// that doesn't was tampered with on the way, and is declined outright.
func (r *ownerRoom) handleJoinRequestPayload(payload *protocol.TransporterMessagePayloadConnectRoom) {
	logger := r.client.Logger
	offer := e2e.KeyExchange{EphemeralKey: payload.EphemeralKey, Signature: payload.Signature}
	if err := e2e.VerifyOffer(r.roomId, payload.PublicKey, offer); err != nil {
		logger.Error(fmt.Sprintf("Declining the join request from %s, it failed verification: %s", payload.ClientId, err))
		emitOwner(r.onEvent, OwnerEvent{Kind: OwnerJoinFailed, GuestClientId: payload.ClientId, Err: fmt.Errorf("the join request failed verification, declined: %w", err)})
		if err := r.client.SendJoinRoomResponse(payload.ClientId, 0, r.ownerIdentity.PublicKey, e2e.KeyExchange{}); err != nil {
			logger.Error(fmt.Sprintf("Failed to decline the join request from %s: %s", payload.ClientId, err))
		}
		return
	}
	emitOwner(r.onEvent, OwnerEvent{Kind: OwnerJoinRequested, GuestClientId: payload.ClientId, GuestPublicKey: payload.PublicKey})
	// promptAccept commonly blocks on user input; run it off the dispatch
	// loop so an already-connected guest's ADB traffic keeps flowing while
	// the operator decides.
	go handleJoinRequest(r.client, r.roomId, r.ownerIdentity, r.promptAccept, r.onEvent, payload.ClientId, payload.PublicKey, offer)
}

func handleJoinRequest(client *transportLayer.Client, roomId string, ownerIdentity *identity.Identity, promptAccept AcceptPromptFunc, onEvent OwnerEventFunc, guestClientId string, guestPublicKey []byte, offer e2e.KeyExchange) {
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

	if !accepted {
		if err := client.SendJoinRoomResponse(guestClientId, 0, ownerIdentity.PublicKey, e2e.KeyExchange{}); err != nil {
			logger.Error(fmt.Sprintf("Failed to send the join room response for %s: %s", guestClientId, err))
			emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinFailed, GuestClientId: guestClientId, Err: err})
			return
		}
		emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinDecided, GuestClientId: guestClientId, GuestPublicKey: guestPublicKey, Accepted: false})
		return
	}

	answer, session, err := e2e.Accept(ownerIdentity, roomId, guestPublicKey, offer)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to set up the session with %s: %s", guestClientId, err))
		emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinFailed, GuestClientId: guestClientId, Err: err})
		return
	}
	// Installed before the acceptance goes out: the guest may start sending
	// as soon as it arrives.
	client.SetSession(session)
	if err := client.SendJoinRoomResponse(guestClientId, 1, ownerIdentity.PublicKey, answer); err != nil {
		client.SetSession(nil)
		logger.Error(fmt.Sprintf("Failed to send the join room response for %s: %s", guestClientId, err))
		emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinFailed, GuestClientId: guestClientId, Err: err})
		return
	}
	emitOwner(onEvent, OwnerEvent{Kind: OwnerJoinDecided, GuestClientId: guestClientId, GuestPublicKey: guestPublicKey, Accepted: true})
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
