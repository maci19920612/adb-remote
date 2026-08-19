package roomManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/manager/connectionManager"
	"adb-remote.maci.team/transporter/utils"
	"context"
	"fmt"
	"log/slog"
)

type roomData struct {
	roomId string
	owner  *connectionManager.ClientConnection
	guest  *connectionManager.ClientConnection
}

type RoomManager struct {
	//Dependencies
	connectionManager *connectionManager.ConnectionManager
	logger            *slog.Logger

	//Internal state
	rooms      []*roomData
	cancelFunc context.CancelFunc
}

func CreateRoomManager(cm *connectionManager.ConnectionManager, logger *slog.Logger) *RoomManager {
	logger.Info("Create room manager")
	ctx, cancelFunc := context.WithCancel(context.Background())
	roomManager := &RoomManager{
		connectionManager: cm,
		logger:            logger,
		rooms:             make([]*roomData, 0, 10),
		cancelFunc:        cancelFunc,
	}

	go roomManager.run(ctx)

	return roomManager
}

// Stop ends the room manager's dispatch loop.
func (rm *RoomManager) Stop() {
	rm.cancelFunc()
}

func (rm *RoomManager) run(ctx context.Context) {
	logger := rm.logger
	cm := rm.connectionManager
	for {
		select {
		case <-ctx.Done():
			return
		case client := <-cm.ClientDisconnectedChannel:
			logger.Info(fmt.Sprintf("RoomManager: Client disconnected: %p", client))
			rm.handleClientDisconnected(client)
		case messageContainer := <-cm.ClientMessageChannel:
			rm.dispatchMessage(messageContainer)
		}
	}
}

func (rm *RoomManager) dispatchMessage(messageContainer *connectionManager.ClientMessageContainer) {
	logger := rm.logger
	defer func() { _ = messageContainer.Message.Dispose() }()

	message, err := messageContainer.Message.Data()
	if err != nil {
		logger.Error(fmt.Sprintf("RoomManager: Unusable pooled message container: %s", err))
		return
	}

	sender := messageContainer.Sender
	logger.Info(fmt.Sprintf("RoomManager: %x message received from client: %p", message.Command(), sender))
	switch message.Command() {
	case protocol.CommandCreateRoom:
		rm.handleCreateRoom(sender)
	case protocol.CommandJoinRoom:
		payload, err := message.GetPayloadConnectRoom()
		if err != nil {
			if err := sender.SendInvalidPayloadError(message.Command()); err != nil {
				_ = sender.Close()
			}
			return
		}
		rm.handleJoinRoom(sender, payload.RoomId, payload.PublicKey)
	case protocol.CommandJoinRoom | protocol.CommandResponseMask:
		payload, err := message.GetPayloadConnectRoomResponse()
		if err != nil {
			logger.Info(fmt.Sprintf("Invalid message payload: %s", err))
			if err := sender.SendInvalidPayloadError(message.Command()); err != nil {
				_ = sender.Close()
			}
			return
		}
		rm.handleJoinRoomResponse(sender, payload.Accepted, payload.PublicKey)
	case protocol.CommandAdbTransport:
		rm.handleAdbTransport(sender, message)
	default:
		logger.Warn(fmt.Sprintf("RoomManager: Unhandled command from client %p: %x", sender, message.Command()))
	}
}

func (rm *RoomManager) handleCreateRoom(sender *connectionManager.ClientConnection) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("%p (%s): Create room request", sender, sender.GetClientId()))
	if rm.isClientInARoom(sender) {
		logger.Error(fmt.Sprintf("%p (%s): Client already present in a room, a client can't occupy more than 1 room", sender, sender.GetClientId()))
		if err := sender.SendErrorResponse(protocol.CommandCreateRoom, protocol.ErrorAlreadyInRoom, "You already occupy a room"); err != nil {
			logger.Error(fmt.Sprintf("%p (%s): Error during the error response sending, close the client connection", sender, sender.GetClientId()))
			_ = sender.Close()
		}
		return
	}
	roomId := utils.GenerateClientId()
	logger.Info(fmt.Sprintf("%p (%s): Room ID generated: %s", sender, sender.GetClientId(), roomId))
	rd := &roomData{
		owner:  sender,
		guest:  nil,
		roomId: roomId,
	}
	rm.rooms = append(rm.rooms, rd)
	if err := sender.SendRoomCreateResponse(roomId); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the room creation response sending: %s", sender, sender.GetClientId(), err))
		_ = sender.Close()
		return
	}
	logger.Info(fmt.Sprintf("%p (%s): Room created: %s", sender, sender.GetClientId(), roomId))
}

func (rm *RoomManager) handleJoinRoom(sender *connectionManager.ClientConnection, roomId string, guestPublicKey []byte) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("%p (%s): Join room request: %s", sender, sender.GetClientId(), roomId))

	targetRoom := rm.findRoomById(roomId)
	if targetRoom == nil {
		logger.Error(fmt.Sprintf("%p (%s): Client can't connect to the room %s: The room does not exist", sender, sender.GetClientId(), roomId))
		if err := sender.SendErrorResponse(
			protocol.CommandJoinRoom,
			protocol.ErrorRoomNotFound,
			fmt.Sprintf("Room not found with this id: %s", roomId),
		); err != nil {
			logger.Error(fmt.Sprintf("%p (%s): Error during the error response sending: %s", sender, sender.GetClientId(), err))
			_ = sender.Close()
		}
		return
	}

	if targetRoom.guest != nil {
		logger.Error(fmt.Sprintf("%p (%s): Client can't join room %s: it already has a guest", sender, sender.GetClientId(), roomId))
		if err := sender.SendErrorResponse(protocol.CommandJoinRoom, protocol.ErrorFull, "This room already has a guest; only one guest is allowed per room"); err != nil {
			logger.Error(fmt.Sprintf("%p (%s): Error during the error response sending: %s", sender, sender.GetClientId(), err))
			_ = sender.Close()
		}
		return
	}

	targetRoom.guest = sender
	owner := targetRoom.owner
	if err := owner.SendJoinRoomRequest(roomId, sender.GetClientId(), guestPublicKey); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the join room request sending to the room owner: %s", owner, owner.GetClientId(), err))
		if err := sender.SendErrorResponse(protocol.CommandJoinRoom, protocol.ErrorUnknown, "Couldn't send the join request to the room owner, closing down the room"); err != nil {
			logger.Error(fmt.Sprintf("%p (%s): Error sending the failure notice to the guest: %s", sender, sender.GetClientId(), err))
		}
		rm.closeRoom(targetRoom)
	}
}

func (rm *RoomManager) handleJoinRoomResponse(sender *connectionManager.ClientConnection, isAccepted int, ownerPublicKey []byte) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("%p (%s): Handle join room response", sender, sender.GetClientId()))

	targetRoom := rm.findRoomByOwner(sender)
	if targetRoom == nil {
		logger.Error(fmt.Sprintf("%p (%s): Room not found by owner", sender, sender.GetClientId()))
		if err := sender.SendErrorResponse(protocol.CommandJoinRoom, protocol.ErrorRoomNotFound, "No room found where the sender is the owner"); err != nil {
			_ = sender.Close()
			logger.Error(fmt.Sprintf("%p (%s): Error during the error response sending: %s", sender, sender.GetClientId(), err))
		}
		return
	}

	if targetRoom.guest == nil {
		logger.Error(fmt.Sprintf("%p (%s): Room was empty", sender, sender.GetClientId()))
		if err := sender.SendErrorResponse(protocol.CommandJoinRoom, protocol.ErrorNoParticipant, "You are in an empty room"); err != nil {
			logger.Error(fmt.Sprintf("%p (%s): Error during the error response sending: %s", sender, sender.GetClientId(), err))
			rm.closeRoom(targetRoom)
		}
		return
	}

	if err := targetRoom.guest.SendJoinRoomResponse(isAccepted, sender.GetClientId(), ownerPublicKey); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the response sending to the guest", sender, sender.GetClientId()))
		_ = targetRoom.guest.Close()
		targetRoom.guest = nil

		if err := sender.SendErrorResponse(protocol.CommandJoinRoom, protocol.ErrorNoParticipant, "participant disconnected during the response sending, the room is waiting for another participant"); err != nil {
			rm.closeRoom(targetRoom)
		}
		return
	}

	if isAccepted == 0 {
		logger.Info(fmt.Sprintf("%p (%s): Join room request declined, evicting the guest", sender, sender.GetClientId()))
		targetRoom.guest = nil
		return
	}

	logger.Info(fmt.Sprintf("%p (%s): The room %s is ready to relay ADB messages", sender, sender.GetClientId(), targetRoom.roomId))
}

// handleAdbTransport forwards an opaque ADB transport message from the
// sender to the other participant in the sender's room. The transporter
// never inspects the embedded ADB payload; it only routes it.
func (rm *RoomManager) handleAdbTransport(sender *connectionManager.ClientConnection, message *protocol.TransporterMessage) {
	logger := rm.logger
	targetRoom := rm.findRoomByParticipant(sender)
	if targetRoom == nil {
		logger.Warn(fmt.Sprintf("%p (%s): Received an ADB transport message outside of any room", sender, sender.GetClientId()))
		return
	}

	var target *connectionManager.ClientConnection
	if targetRoom.owner == sender {
		target = targetRoom.guest
	} else {
		target = targetRoom.owner
	}
	if target == nil {
		logger.Warn(fmt.Sprintf("%p (%s): Received an ADB transport message but the room has no other participant", sender, sender.GetClientId()))
		return
	}

	if err := target.Send(message); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Failed to forward the ADB transport message: %s", sender, sender.GetClientId(), err))
	}
}

func (rm *RoomManager) closeRoom(room *roomData) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("Room closed: %s", room.roomId))
	if room.owner != nil {
		logger.Info(fmt.Sprintf("%p (%s): Disconnecting client due to room close", room.owner, room.owner.GetClientId()))
		_ = room.owner.Close()
	}
	if room.guest != nil {
		logger.Info(fmt.Sprintf("%p (%s): Disconnecting client due to room close", room.guest, room.guest.GetClientId()))
		_ = room.guest.Close()
	}

	targetIndex := -1
	for index, candidate := range rm.rooms {
		if candidate == room {
			targetIndex = index
			break
		}
	}
	if targetIndex >= 0 {
		rm.rooms = append(rm.rooms[:targetIndex], rm.rooms[targetIndex+1:]...)
	} else {
		logger.Warn("Room not found in the room manager")
	}
	logger.Info(fmt.Sprintf("Room deleted: %s", room.roomId))
}

func (rm *RoomManager) isClientInARoom(connection *connectionManager.ClientConnection) bool {
	return rm.findRoomByParticipant(connection) != nil
}

func (rm *RoomManager) findRoomById(roomId string) *roomData {
	for _, room := range rm.rooms {
		if room.roomId == roomId {
			return room
		}
	}
	return nil
}

func (rm *RoomManager) findRoomByOwner(connection *connectionManager.ClientConnection) *roomData {
	for _, room := range rm.rooms {
		if room.owner == connection {
			return room
		}
	}
	return nil
}

func (rm *RoomManager) findRoomByParticipant(connection *connectionManager.ClientConnection) *roomData {
	for _, room := range rm.rooms {
		if room.owner == connection || room.guest == connection {
			return room
		}
	}
	return nil
}

func (rm *RoomManager) handleClientDisconnected(client *connectionManager.ClientConnection) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("Client disconnected: %p", client))

	targetRoom := rm.findRoomByParticipant(client)
	if targetRoom == nil {
		logger.Info(fmt.Sprintf("The disconnected client did not join a room: %p", client))
		return
	}
	if targetRoom.owner == client {
		logger.Info(fmt.Sprintf("The disconnected client was the room (%s) owner, closing the room: %p", targetRoom.roomId, client))
		rm.closeRoom(targetRoom)
	} else if targetRoom.guest == client {
		logger.Info(fmt.Sprintf("The disconnected client was the room (%s) guest, clearing the room: %p", targetRoom.roomId, client))
		_ = targetRoom.guest.Close()
		targetRoom.guest = nil
	}
}
