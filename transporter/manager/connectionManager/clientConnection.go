package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/utils"
	"fmt"
	"io"
	"net"
)

type ClientConnection struct {
	connection  net.Conn
	owner       *ConnectionManager
	clientId    string
	isConnected bool
}

func (cc *ClientConnection) internalClose() {
	if !cc.isConnected {
		return
	}
	cc.isConnected = false
	cc.owner.internalCloseClient(cc)
}

func (cc *ClientConnection) GetClientId() string {
	return cc.clientId
}

func (cc *ClientConnection) start() {
	go cc.run()
}

func (cc *ClientConnection) run() {
	logger := cc.owner.logger
	logger.Info(fmt.Sprintf("%p (-): Client connection started", cc))

	if !cc.performHandshake() {
		return
	}

	for {
		logger.Info(fmt.Sprintf("%p (%s): Waiting for message", cc, cc.GetClientId()))
		if !cc.readNextMessage() {
			return
		}
	}
}

// performHandshake consumes the initial CNXN message and replies with the
// assigned client id. It returns false if the connection was closed as part
// of handling the handshake (either because it failed, or because the
// caller's protocol version was rejected).
func (cc *ClientConnection) performHandshake() bool {
	logger := cc.owner.logger
	pool := cc.owner.transporterMessagePool

	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		logger.Error(fmt.Sprintf("%p (-): Unusable pooled message container: %s", cc, err))
		cc.internalClose()
		return false
	}

	if err := message.Read(cc.connection); err != nil {
		logger.Error(fmt.Sprintf("%p (-): Error during the transporter message reading: %s", cc, err))
		cc.internalClose()
		return false
	}

	switch message.Command() {
	case protocol.CommandConnect:
		return cc.handleConnectHandshake(message)
	case protocol.CommandReconnect:
		logger.Info(fmt.Sprintf("%p (-): Reconnect is not implemented yet", cc))
		cc.internalClose()
		return false
	default:
		logger.Error(fmt.Sprintf("%p (-): Client attempted an invalid handshake, closing the connection quietly", cc))
		cc.internalClose()
		return false
	}
}

func (cc *ClientConnection) handleConnectHandshake(message *protocol.TransporterMessage) bool {
	logger := cc.owner.logger

	payload, err := message.GetPayloadConnect()
	if err != nil {
		logger.Error(fmt.Sprintf("%p (-): Error during the connect payload reading: %s", cc, err))
		cc.internalClose()
		return false
	}
	if payload.ProtocolVersion != protocol.ProtocolVersion {
		logger.Error(fmt.Sprintf("%p (-): Protocol version mismatch: server=%d, client=%d", cc, protocol.ProtocolVersion, payload.ProtocolVersion))
		cc.handleProtocolMismatchError(payload.ProtocolVersion)
		return false
	}

	logger.Info(fmt.Sprintf("%p (-): A client started the connection process", cc))
	clientId := utils.GenerateClientId()
	logger.Info(fmt.Sprintf("%p (%s): Client ID generated", cc, clientId))
	cc.clientId = clientId

	message.SetResponseCommand(protocol.CommandConnect)
	if err := message.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{
		ClientId: clientId,
	}); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the connect response payload creation: %s", cc, clientId, err))
		cc.internalClose()
		return false
	}
	if err := message.Write(cc.connection); err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the connect response payload sending: %s", cc, clientId, err))
		cc.internalClose()
		return false
	}
	logger.Info(fmt.Sprintf("%p (%s): Client connection established", cc, clientId))
	return true
}

// readNextMessage reads a single message and forwards it on the owning
// ConnectionManager's ClientMessageChannel. It returns false once the
// connection should stop being read from (on close or unrecoverable error).
func (cc *ClientConnection) readNextMessage() bool {
	logger := cc.owner.logger
	pool := cc.owner.transporterMessagePool

	container := pool.Obtain()
	message, err := container.Data()
	if err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Unusable pooled message container: %s", cc, cc.GetClientId(), err))
		_ = container.Dispose()
		cc.internalClose()
		return false
	}

	if err := message.Read(cc.connection); err != nil {
		if err == io.EOF {
			logger.Info(fmt.Sprintf("%p (%s): Client disconnected", cc, cc.GetClientId()))
		} else {
			logger.Error(fmt.Sprintf("%p (%s): Invalid message read from the network: %s", cc, cc.GetClientId(), err))
		}
		_ = container.Dispose()
		cc.internalClose()
		return false
	}

	logger.Info(fmt.Sprintf("%p (%s): Message received from the client: %x", cc, cc.GetClientId(), message.Command()))
	cc.owner.ClientMessageChannel <- &ClientMessageContainer{
		Sender:  cc,
		Message: container,
	}
	// Ownership of the container passes to whoever consumes
	// ClientMessageChannel; they are responsible for disposing of it.
	return true
}

func (cc *ClientConnection) Send(message *protocol.TransporterMessage) error {
	return message.Write(cc.connection)
}

func (cc *ClientConnection) Close() error {
	cc.internalClose()
	return nil
}

func (cc *ClientConnection) SendErrorResponse(command uint32, errorCode int, errorMessage string) error {
	pool := cc.owner.transporterMessagePool
	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
	message.SetErrorResponseCommand(command)
	if err := message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	}); err != nil {
		return err
	}
	return message.Write(cc.connection)
}

func (cc *ClientConnection) SendRoomCreateResponse(roomId string) error {
	pool := cc.owner.transporterMessagePool
	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
	message.SetResponseCommand(protocol.CommandCreateRoom)
	if err := message.SetPayloadCreateRoomResponse(&protocol.TransporterMessagePayloadCreateRoomResponse{
		RoomId: roomId,
	}); err != nil {
		return err
	}
	return message.Write(cc.connection)
}

func (cc *ClientConnection) SendJoinRoomRequest(roomId string, clientId string) error {
	pool := cc.owner.transporterMessagePool
	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
	message.SetDirectCommand(protocol.CommandJoinRoom)
	if err := message.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{
		RoomId:   roomId,
		ClientId: clientId,
	}); err != nil {
		return err
	}
	return message.Write(cc.connection)
}

func (cc *ClientConnection) SendJoinRoomResponse(isAccepted int) error {
	pool := cc.owner.transporterMessagePool
	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
	message.SetResponseCommand(protocol.CommandJoinRoom)
	if err := message.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{
		Accepted: isAccepted,
	}); err != nil {
		return err
	}
	return message.Write(cc.connection)
}

func (cc *ClientConnection) SendInvalidPayloadError(command uint32) error {
	pool := cc.owner.transporterMessagePool
	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
	message.SetErrorResponseCommand(command)
	if err := message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorMessage: "Invalid command payload",
		ErrorCode:    protocol.ErrorInvalidPayload,
	}); err != nil {
		return err
	}
	return message.Write(cc.connection)
}

func (cc *ClientConnection) handleProtocolMismatchError(clientProtocolVersion uint32) {
	logger := cc.owner.logger
	pool := cc.owner.transporterMessagePool
	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		logger.Error(fmt.Sprintf("%p (-): Unusable pooled message container: %s", cc, err))
		cc.internalClose()
		return
	}

	logger.Error(fmt.Sprintf("Protocol version not supported, transporter: %d, client: %d", protocol.ProtocolVersion, clientProtocolVersion))
	message.SetErrorResponseCommand(protocol.CommandConnect)
	if err := message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    protocol.ErrorProtocolNotSupported,
		ErrorMessage: fmt.Sprintf("Protocol version mismatch, transporter: %d, client: %d", protocol.ProtocolVersion, clientProtocolVersion),
	}); err != nil {
		logger.Error(fmt.Sprintf("Error during the error payload creation: %s", err))
	} else if err := message.Write(cc.connection); err != nil {
		logger.Error(fmt.Sprintf("Error during the message sending to the client: %s", err))
	}
	cc.owner.internalCloseClient(cc)
}
