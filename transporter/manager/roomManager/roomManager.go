package roomManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/manager/connectionManager"
	"adb-remote.maci.team/transporter/utils"
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
	connectionManager *connectionManager.IConnectionManager
	logger            *slog.Logger

	//Internal state
	rooms []*roomData
}

func CreateRoomManager(cm *connectionManager.IConnectionManager, logger *slog.Logger) *RoomManager {
	roomManager := &RoomManager{
		connectionManager: cm,
		logger:            logger,
		rooms:             make([]*roomData, 0, 10),
	}
	(*cm).RegisterOnClientConnectedCallback(func(connection *connectionManager.ClientConnection) {

	})
	(*cm).RegisterOnClientDisconnectedCallback(func(connection *connectionManager.ClientConnection) {

	})
	(*cm).RegisterOnClientMessageCallback(func(connection *connectionManager.ClientConnection, message *protocol.TransporterMessage) {
		switch message.Command() {
		case protocol.CommandCreateRoom:
			roomManager.handleCreateRoom(connection)
		}
	})

	return roomManager
}

func (rm *RoomManager) handleCreateRoom(sender *connectionManager.ClientConnection) {
	//TODO: Mutex needed here
	logger := rm.logger
	logger.Info("%p (%s): Create room request", sender, sender.GetClientId())
	if checkClientAlreadyInARoom(rm, sender) {
		logger.Error("%p (%s): Client already present in a roomData, a client can't occupy more than 1 roomData", sender, sender.GetClientId())
		err := sender.SendErrorResponse(protocol.CommandCreateRoom, protocol.ErrorAlreadyInRoom, "You are already occupy a roomData")
		if err != nil {
			logger.Error("%p (%s): Client error during the error response sending, close the client connection")
			_ = sender.Close()
		}
		return
	}
	roomId := utils.GenerateClientId()
	logger.Info("%p (%s): Room ID generated: %s", sender, sender.GetClientId(), roomId)
	rd := &roomData{
		owner:  sender,
		guest:  nil,
		roomId: roomId,
	}
	rm.rooms = append(rm.rooms, rd)
	err := sender.SendRoomCreateResponse(roomId)
	if err != nil {
		logger.Error("%p (%s): Error during the room creation response sending: %s", sender, sender.GetClientId(), err)
		_ = sender.Close()
		return
	}
	logger.Info("%p (%s): Room created: %s", sender, sender.GetClientId(), roomId)
}

func (rm *RoomManager) handleJoinRoom(sender *connectionManager.ClientConnection, roomId string) {
	//TODO: Mutex needed here
	logger := rm.logger
	logger.Info("%p (%s): Join room request")
	targetRoomIndex := -1
	for index, room := range rm.rooms {
		if room.roomId == roomId {
			targetRoomIndex = index
		}
	}
	if targetRoomIndex == -1 {
		logger.Error("%p (%s): Client can't connect to the room %s: The room does not exists", sender, sender.GetClientId(), roomId)
		err := sender.SendErrorResponse(
			protocol.CommandConnectRoom,
			protocol.ErrorRoomNotFound,
			fmt.Sprintf("Room not found with this id: %s", roomId),
		)
		if err != nil {
			logger.Error("%p (%s): Error during the error response sending: %s", sender, sender.GetClientId(), err)
			_ = sender.Close()
			return
		}
		return
	}
	targetRoom := rm.rooms[targetRoomIndex]
	targetRoom.guest = sender
	owner := targetRoom.owner
	err := owner.SendJoinRoomRequest(roomId, sender.GetClientId())
	if err != nil {
		logger.Error("%p (%s): Error during the send join room request sending to the room owner: %s", owner, owner.GetClientId(), err)
		err := sender.SendErrorResponse(protocol.CommandConnectRoom, protocol.ErrorUnknown, "Couldn't send the join request to the room owner, closing down the room")
		if err != nil {
			logger.Error("%p (%s): SendError", sender, sender.GetClientId())
		}
		rm.closeRoom(targetRoom)
	}
}

func (rm *RoomManager) handleJoinRoomResponse(sender *connectionManager.ClientConnection, isAccepted int) {
	logger := rm.logger
	logger.Info("%p (%s): Handle join room response", sender, sender.GetClientId())
	var targetRoom *roomData = nil
	for _, room := range rm.rooms {
		if room.owner == sender {
			targetRoom = room
		}
	}
	if targetRoom == nil {
		logger.Error("%p (%s): Room not found by owner", sender, sender.GetClientId())
		err := sender.SendErrorResponse(protocol.CommandConnectRoomResult, protocol.ErrorRoomNotFound, fmt.Sprintf("No room found where the sender is the owner"))
		if err != nil {
			_ = sender.Close()
			logger.Error("%p (%s): Error during the error response sending: %s", sender, sender.GetClientId(), err)
		}
		return
	}

	if targetRoom.guest == nil {
		logger.Error("%p (%s): Room was empty", sender, sender.GetClientId())
		err := sender.SendErrorResponse(protocol.CommandConnectRoomResult, protocol.ErrorNoParticipant, fmt.Sprintf("You are in an empty room"))
		if err != nil {
			logger.Error("%p (%s): Error during the error response sending %s", sender, sender.GetClientId(), err)
			rm.closeRoom(targetRoom)
		}
		return
	}
	err := targetRoom.guest.SendJoinRoomResponse(isAccepted)
	if err != nil {
		logger.Error("%p (%s): Error during the response sending to the guest", sender, sender.GetClientId())
		_ = targetRoom.guest.Close()
		targetRoom.guest = nil

		err = sender.SendErrorResponse(protocol.CommandConnectRoomResult, protocol.ErrorNoParticipant, "participant disconnected during the response sending, the room is waiting for an another participant")
		if err != nil {
			rm.closeRoom(targetRoom)
		}
	}

	logger.Info("%p (%s): The room is ready to transport the ADB messages in the room")
}

func (rm *RoomManager) closeRoom(roomData *roomData) {
	logger := rm.logger
	logger.Info("%p (%s): Room closed", roomData, roomData.roomId)
	owner := roomData.owner
	guest := roomData.guest
	if owner != nil {
		logger.Info("%p (%s): Disconnect from client due to room close", owner, owner.GetClientId())
		_ = owner.Close()
	}
	if guest != nil {
		logger.Info("%p (%s): Disconnect from client due to room close", guest, guest.GetClientId())
		_ = guest.Close()
	}

	targetIndex := -1
	for index, roomDataItem := range rm.rooms {
		if roomDataItem == roomData {
			targetIndex = index
		}
	}
	if targetIndex >= 0 {
		rm.rooms = append(rm.rooms[:targetIndex], rm.rooms[targetIndex+1:]...)
	} else {
		logger.Warn("Room not found in the room manager")
	}
	logger.Info("%p (%s): Room deleted", roomData, roomData.roomId)
}

func checkClientAlreadyInARoom(roomManager *RoomManager, connection *connectionManager.ClientConnection) bool {
	for _, rm := range (*roomManager).rooms {
		if rm.guest == connection || rm.owner == connection {
			return true
		}
	}
	return false
}

func (rm *RoomManager) handleClientDisconnected() {
}
