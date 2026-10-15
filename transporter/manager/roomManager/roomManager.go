package roomManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/config"
	"adb-remote.maci.team/transporter/manager/connectionManager"
	"adb-remote.maci.team/transporter/utils"
	"context"
	"fmt"
	"log/slog"
	"time"
)

// A client address may miss maxFailedJoinsPerWindow join requests (to room
// ids that don't exist) per failedJoinWindow; after that its join requests
// are refused until the window ends. Room ids are the secret that gates a
// room, so guessing them must stay impractical.
const (
	maxFailedJoinsPerWindow = 10
	failedJoinWindow        = time.Minute
)

// maxSweepInterval bounds how often the room manager looks for expired
// rooms and stale join statistics.
const maxSweepInterval = time.Minute

type roomData struct {
	roomId string
	owner  *connectionManager.ClientConnection
	guest  *connectionManager.ClientConnection
	// guestAccepted is set once the owner accepted the current guest; ADB
	// traffic is only relayed after that.
	guestAccepted bool
	// idleSince is when the room last became without an accepted guest
	// (or was created); it expires once that lasts for too long.
	idleSince time.Time
}

// failedJoins counts one address's failed join requests in the current
// window.
type failedJoins struct {
	count       int
	windowStart time.Time
}

type RoomManager struct {
	//Dependencies
	connectionManager *connectionManager.ConnectionManager
	config            *config.TransporterConfiguration
	logger            *slog.Logger

	//Internal state, only touched by the run goroutine
	rooms         map[string]*roomData
	roomsByClient map[*connectionManager.ClientConnection]*roomData // owner or guest
	roomsPerIP    map[string]int                                    // by the owner's address
	failedJoins   map[string]*failedJoins
	cancelFunc    context.CancelFunc
}

func CreateRoomManager(cm *connectionManager.ConnectionManager, configuration *config.TransporterConfiguration, logger *slog.Logger) *RoomManager {
	logger.Info("Create room manager")
	ctx, cancelFunc := context.WithCancel(context.Background())
	roomManager := &RoomManager{
		connectionManager: cm,
		config:            configuration,
		logger:            logger,
		rooms:             make(map[string]*roomData),
		roomsByClient:     make(map[*connectionManager.ClientConnection]*roomData),
		roomsPerIP:        make(map[string]int),
		failedJoins:       make(map[string]*failedJoins),
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
	sweepInterval := maxSweepInterval
	if idleTimeout := rm.config.RoomIdleTimeoutDuration(); idleTimeout > 0 && idleTimeout/4 < sweepInterval {
		sweepInterval = max(idleTimeout/4, 10*time.Millisecond)
	}
	sweepTicker := time.NewTicker(sweepInterval)
	defer sweepTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case client := <-cm.ClientDisconnectedChannel:
			logger.Info(fmt.Sprintf("RoomManager: Client disconnected: %p", client))
			rm.handleClientDisconnected(client)
		case messageContainer := <-cm.ClientMessageChannel:
			rm.dispatchMessageSafely(messageContainer)
		case now := <-sweepTicker.C:
			rm.sweep(now)
		}
	}
}

// dispatchMessageSafely wraps dispatchMessage with a recover. dispatchMessage
// (and the payload parsing it triggers) runs on this single goroutine shared
// across every room and client in the process, so a panic triggered by one
// malformed message from one client — a parsing bug, not necessarily this
// specific one — would otherwise crash the transporter for everyone instead
// of just that one client's connection.
func (rm *RoomManager) dispatchMessageSafely(messageContainer *connectionManager.ClientMessageContainer) {
	defer func() {
		if r := recover(); r != nil {
			sender := messageContainer.Sender
			rm.logger.Error(fmt.Sprintf("RoomManager: recovered from a panic handling a message from %p (%s), closing the connection: %v", sender, sender.GetClientId(), r))
			_ = sender.Close()
		}
	}()
	rm.dispatchMessage(messageContainer)
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
	if sender.IsClosed() {
		// Read before the connection closed; its disconnect is handled on
		// its own, and nothing it asked for can be answered any more.
		return
	}
	switch message.Command() {
	case protocol.CommandCreateRoom:
		rm.handleCreateRoom(sender)
	case protocol.CommandJoinRoom:
		payload, err := message.GetPayloadConnectRoom()
		if err == nil {
			err = payload.ValidateKeys()
		}
		if err != nil {
			logger.Info(fmt.Sprintf("%p (%s): Invalid join room request: %s", sender, sender.GetClientId(), err))
			rm.replyInvalidPayload(sender, message.Command())
			return
		}
		rm.handleJoinRoom(sender, payload)
	case protocol.CommandJoinRoom | protocol.CommandResponseMask:
		payload, err := message.GetPayloadConnectRoomResponse()
		if err == nil {
			err = payload.ValidateKeys()
		}
		if err != nil {
			logger.Info(fmt.Sprintf("%p (%s): Invalid join room response: %s", sender, sender.GetClientId(), err))
			rm.replyInvalidPayload(sender, message.Command())
			return
		}
		rm.handleJoinRoomResponse(sender, payload)
	case protocol.CommandAdbTransport:
		rm.handleAdbTransport(sender, messageContainer.Message)
	default:
		logger.Warn(fmt.Sprintf("RoomManager: Unhandled command from client %p: %x", sender, message.Command()))
	}
}

func (rm *RoomManager) replyInvalidPayload(sender *connectionManager.ClientConnection, command uint32) {
	if err := sender.SendInvalidPayloadError(command); err != nil {
		_ = sender.Close()
	}
}

// replyError sends an error response, closing the connection if even that
// fails.
func (rm *RoomManager) replyError(sender *connectionManager.ClientConnection, command uint32, errorCode int, errorMessage string) {
	if err := sender.SendErrorResponse(command, errorCode, errorMessage); err != nil {
		rm.logger.Error(fmt.Sprintf("%p (%s): Error during the error response sending, closing the client connection: %s", sender, sender.GetClientId(), err))
		_ = sender.Close()
	}
}

func (rm *RoomManager) handleCreateRoom(sender *connectionManager.ClientConnection) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("%p (%s): Create room request", sender, sender.GetClientId()))
	if rm.isClientInARoom(sender) {
		logger.Error(fmt.Sprintf("%p (%s): Client already present in a room, a client can't occupy more than 1 room", sender, sender.GetClientId()))
		rm.replyError(sender, protocol.CommandCreateRoom, protocol.ErrorAlreadyInRoom, "You already occupy a room")
		return
	}
	if len(rm.rooms) >= rm.config.RoomLimit() {
		logger.Warn(fmt.Sprintf("%p (%s): Refusing a new room, the transporter already has %d", sender, sender.GetClientId(), len(rm.rooms)))
		rm.replyError(sender, protocol.CommandCreateRoom, protocol.ErrorLimitReached, "The transporter can't take any more rooms right now")
		return
	}
	ownerIP := sender.RemoteIP()
	if rm.roomsPerIP[ownerIP] >= rm.config.RoomLimitPerIP() {
		logger.Warn(fmt.Sprintf("%p (%s): Refusing a new room, %s already owns %d", sender, sender.GetClientId(), ownerIP, rm.roomsPerIP[ownerIP]))
		rm.replyError(sender, protocol.CommandCreateRoom, protocol.ErrorLimitReached, "Your address already owns too many rooms")
		return
	}

	roomId := utils.GenerateRoomId()
	for rm.rooms[roomId] != nil {
		roomId = utils.GenerateRoomId()
	}
	logger.Info(fmt.Sprintf("%p (%s): Room ID generated: %s", sender, sender.GetClientId(), roomId))
	rd := &roomData{
		owner:     sender,
		roomId:    roomId,
		idleSince: time.Now(),
	}
	rm.rooms[roomId] = rd
	rm.roomsByClient[sender] = rd
	rm.roomsPerIP[ownerIP]++
	if err := sender.SendRoomCreateResponse(roomId); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the room creation response sending: %s", sender, sender.GetClientId(), err))
		rm.closeRoom(rd)
		return
	}
	logger.Info(fmt.Sprintf("%p (%s): Room created: %s", sender, sender.GetClientId(), roomId))
}

func (rm *RoomManager) handleJoinRoom(sender *connectionManager.ClientConnection, request *protocol.TransporterMessagePayloadConnectRoom) {
	logger := rm.logger
	roomId := request.RoomId
	logger.Info(fmt.Sprintf("%p (%s): Join room request: %s", sender, sender.GetClientId(), roomId))

	if rm.isClientInARoom(sender) {
		logger.Error(fmt.Sprintf("%p (%s): Client already present in a room, can't join %s", sender, sender.GetClientId(), roomId))
		rm.replyError(sender, protocol.CommandJoinRoom, protocol.ErrorAlreadyInRoom, "You already occupy a room")
		return
	}
	if rm.tooManyFailedJoins(sender.RemoteIP()) {
		logger.Warn(fmt.Sprintf("%p (%s): Refusing a join request, %s missed too many recently", sender, sender.GetClientId(), sender.RemoteIP()))
		rm.replyError(sender, protocol.CommandJoinRoom, protocol.ErrorTooManyAttempts, "Too many failed join attempts, try again later")
		return
	}

	targetRoom := rm.rooms[roomId]
	if targetRoom == nil {
		logger.Error(fmt.Sprintf("%p (%s): Client can't connect to the room %s: The room does not exist", sender, sender.GetClientId(), roomId))
		rm.recordFailedJoin(sender.RemoteIP())
		rm.replyError(sender, protocol.CommandJoinRoom, protocol.ErrorRoomNotFound, fmt.Sprintf("Room not found with this id: %s", roomId))
		return
	}

	if targetRoom.guest != nil {
		logger.Error(fmt.Sprintf("%p (%s): Client can't join room %s: it already has a guest", sender, sender.GetClientId(), roomId))
		rm.replyError(sender, protocol.CommandJoinRoom, protocol.ErrorFull, "This room already has a guest; only one guest is allowed per room")
		return
	}

	targetRoom.guest = sender
	targetRoom.guestAccepted = false
	rm.roomsByClient[sender] = targetRoom
	owner := targetRoom.owner
	forwarded := *request
	forwarded.RoomId = targetRoom.roomId
	forwarded.ClientId = sender.GetClientId()
	if err := owner.SendJoinRoomRequest(&forwarded); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the join room request sending to the room owner: %s", owner, owner.GetClientId(), err))
		if err := sender.SendErrorResponse(protocol.CommandJoinRoom, protocol.ErrorUnknown, "Couldn't send the join request to the room owner, closing down the room"); err != nil {
			logger.Error(fmt.Sprintf("%p (%s): Error sending the failure notice to the guest: %s", sender, sender.GetClientId(), err))
		}
		rm.closeRoom(targetRoom)
	}
}

func (rm *RoomManager) handleJoinRoomResponse(sender *connectionManager.ClientConnection, result *protocol.TransporterMessagePayloadConnectRoomResult) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("%p (%s): Handle join room response", sender, sender.GetClientId()))

	targetRoom := rm.roomsByClient[sender]
	if targetRoom == nil || targetRoom.owner != sender {
		logger.Error(fmt.Sprintf("%p (%s): Room not found by owner", sender, sender.GetClientId()))
		rm.replyError(sender, protocol.CommandJoinRoom, protocol.ErrorRoomNotFound, "No room found where the sender is the owner")
		return
	}

	guest := targetRoom.guest
	if guest == nil || guest.GetClientId() != result.ClientId {
		// The guest this decision is about left, maybe with another one
		// asking since; the decision must not land on that one.
		logger.Error(fmt.Sprintf("%p (%s): Decision about %q, who is no longer the room's guest", sender, sender.GetClientId(), result.ClientId))
		rm.replyError(sender, protocol.CommandJoinRoom, protocol.ErrorNoParticipant, "The guest this decision is about is no longer in the room")
		return
	}
	if targetRoom.guestAccepted {
		logger.Error(fmt.Sprintf("%p (%s): The room's guest was already accepted", sender, sender.GetClientId()))
		rm.replyInvalidPayload(sender, protocol.CommandJoinRoom|protocol.CommandResponseMask)
		return
	}

	forwarded := *result
	forwarded.ClientId = sender.GetClientId()
	if err := guest.SendJoinRoomResponse(&forwarded); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the response sending to the guest", sender, sender.GetClientId()))
		rm.removeGuest(targetRoom)
		_ = guest.Close()
		rm.replyError(sender, protocol.CommandJoinRoom, protocol.ErrorNoParticipant, "participant disconnected during the response sending, the room is waiting for another participant")
		return
	}

	if result.Accepted == 0 {
		logger.Info(fmt.Sprintf("%p (%s): Join room request declined, evicting the guest", sender, sender.GetClientId()))
		rm.removeGuest(targetRoom)
		guest.CloseAfterFlush()
		return
	}

	targetRoom.guestAccepted = true
	logger.Info(fmt.Sprintf("%p (%s): The room %s is ready to relay ADB messages", sender, sender.GetClientId(), targetRoom.roomId))
}

// handleAdbTransport forwards an opaque ADB transport message from the
// sender to the other participant in the sender's room, once the owner
// accepted the guest. The transporter never inspects the embedded ADB
// payload (it is encrypted end to end anyway); it only routes it.
func (rm *RoomManager) handleAdbTransport(sender *connectionManager.ClientConnection, message *connectionManager.MessageContainer) {
	logger := rm.logger
	targetRoom := rm.roomsByClient[sender]
	if targetRoom == nil {
		logger.Warn(fmt.Sprintf("%p (%s): Received an ADB transport message outside of any room", sender, sender.GetClientId()))
		return
	}
	if !targetRoom.guestAccepted {
		logger.Warn(fmt.Sprintf("%p (%s): Dropping an ADB transport message, the room's guest wasn't accepted", sender, sender.GetClientId()))
		return
	}

	target := targetRoom.owner
	if targetRoom.owner == sender {
		target = targetRoom.guest
	}
	if err := target.Forward(message); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Failed to forward the ADB transport message: %s", sender, sender.GetClientId(), err))
	}
}

// removeGuest takes the guest out of room, which then waits for the next
// one.
func (rm *RoomManager) removeGuest(room *roomData) {
	if room.guest != nil {
		delete(rm.roomsByClient, room.guest)
	}
	room.guest = nil
	room.guestAccepted = false
	room.idleSince = time.Now()
}

// forgetRoom removes room from every index, without closing anything.
func (rm *RoomManager) forgetRoom(room *roomData) {
	if rm.rooms[room.roomId] != room {
		rm.logger.Warn("Room not found in the room manager")
		return
	}
	delete(rm.rooms, room.roomId)
	delete(rm.roomsByClient, room.owner)
	if room.guest != nil {
		delete(rm.roomsByClient, room.guest)
	}
	ownerIP := room.owner.RemoteIP()
	rm.roomsPerIP[ownerIP]--
	if rm.roomsPerIP[ownerIP] <= 0 {
		delete(rm.roomsPerIP, ownerIP)
	}
	rm.logger.Info(fmt.Sprintf("Room deleted: %s", room.roomId))
}

func (rm *RoomManager) closeRoom(room *roomData) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("Room closed: %s", room.roomId))
	rm.forgetRoom(room)
	logger.Info(fmt.Sprintf("%p (%s): Disconnecting client due to room close", room.owner, room.owner.GetClientId()))
	_ = room.owner.Close()
	if room.guest != nil {
		logger.Info(fmt.Sprintf("%p (%s): Disconnecting client due to room close", room.guest, room.guest.GetClientId()))
		_ = room.guest.Close()
	}
}

// sweep closes the rooms that went without an accepted guest for too long,
// telling their owners why, and forgets stale join statistics.
func (rm *RoomManager) sweep(now time.Time) {
	if idleTimeout := rm.config.RoomIdleTimeoutDuration(); idleTimeout > 0 {
		for _, room := range rm.rooms {
			if room.guestAccepted || now.Sub(room.idleSince) < idleTimeout {
				continue
			}
			rm.logger.Info(fmt.Sprintf("Room %s had no guest for %s, closing it", room.roomId, idleTimeout))
			rm.forgetRoom(room)
			if err := room.owner.SendErrorResponse(protocol.CommandCreateRoom, protocol.ErrorRoomExpired, fmt.Sprintf("The room was closed after %s without a guest", idleTimeout)); err == nil {
				room.owner.CloseAfterFlush()
			} else {
				_ = room.owner.Close()
			}
			if room.guest != nil {
				_ = room.guest.Close()
			}
		}
	}
	for ip, attempts := range rm.failedJoins {
		if now.Sub(attempts.windowStart) >= failedJoinWindow {
			delete(rm.failedJoins, ip)
		}
	}
}

func (rm *RoomManager) tooManyFailedJoins(ip string) bool {
	attempts := rm.failedJoins[ip]
	if attempts == nil {
		return false
	}
	if time.Since(attempts.windowStart) >= failedJoinWindow {
		delete(rm.failedJoins, ip)
		return false
	}
	return attempts.count >= maxFailedJoinsPerWindow
}

func (rm *RoomManager) recordFailedJoin(ip string) {
	attempts := rm.failedJoins[ip]
	if attempts == nil || time.Since(attempts.windowStart) >= failedJoinWindow {
		attempts = &failedJoins{windowStart: time.Now()}
		rm.failedJoins[ip] = attempts
	}
	attempts.count++
}

func (rm *RoomManager) isClientInARoom(connection *connectionManager.ClientConnection) bool {
	return rm.roomsByClient[connection] != nil
}

func (rm *RoomManager) handleClientDisconnected(client *connectionManager.ClientConnection) {
	logger := rm.logger
	logger.Info(fmt.Sprintf("Client disconnected: %p", client))

	targetRoom := rm.roomsByClient[client]
	if targetRoom == nil {
		logger.Info(fmt.Sprintf("The disconnected client did not join a room: %p", client))
		return
	}
	if targetRoom.owner == client {
		logger.Info(fmt.Sprintf("The disconnected client was the room (%s) owner, closing the room: %p", targetRoom.roomId, client))
		rm.closeRoom(targetRoom)
	} else if targetRoom.guest == client {
		logger.Info(fmt.Sprintf("The disconnected client was the room (%s) guest, clearing the room: %p", targetRoom.roomId, client))
		rm.removeGuest(targetRoom)
		if err := targetRoom.owner.SendGuestLeft(); err != nil {
			logger.Error(fmt.Sprintf("%p (%s): Failed to notify the owner that the guest left: %s", targetRoom.owner, targetRoom.owner.GetClientId(), err))
		}
	}
}
