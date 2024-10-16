package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/utils"
	"fmt"
	"log"
	"net"
)

type ClientConnection struct {
	connection *net.Conn
	owner      *ConnectionManager
	clientId   string
}

func (cc *ClientConnection) GetClientId() string {
	return cc.clientId
}

func (clientConnection *ClientConnection) start() {
	logger := clientConnection.owner.logger
	messagePool := clientConnection.owner.transporterMessagePool
	logger.Info("%p (-): Client connection started")
	go func() {
		connectTransporterMessage := messagePool.Obtain()
		err := connectTransporterMessage.Read(clientConnection.connection)
		if err != nil {
			logger.Error("%p (-): Error during the transporter message reading: %s", clientConnection, err)
			clientConnection.internalClose()
			messagePool.Release(connectTransporterMessage)
			return
		}
		switch connectTransporterMessage.Command() {
		case protocol.CommandConnect:
			payload, err := connectTransporterMessage.GetPayloadConnect()
			if err != nil {
				logger.Error("%p (-): Error during the connect payload reading: %s", clientConnection, err)
				clientConnection.internalClose()
				messagePool.Release(connectTransporterMessage)
				return
			}
			//ERROR: Protocol version not supported
			if payload.ProtocolVersion != protocol.ProtocolVersion {
				logger.Error("%p (-): Protocol version mismatch: server=%d, client=%d", clientConnection, protocol.ProtocolVersion, payload.ProtocolVersion)
				clientConnection.handleProtocolMismatchError(payload.ProtocolVersion)
				messagePool.Release(connectTransporterMessage)
				return
			}
			logger.Info("%p (-): A client started the connection process", clientConnection)
			clientId := utils.GenerateClientId()
			logger.Info("%p (%s): Client ID generated:", clientConnection, clientId)
			clientConnection.clientId = clientId
			connectTransporterMessage.SetResponseCommand(protocol.CommandConnect)
			err = connectTransporterMessage.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{
				ClientId: clientId,
			})
			if err != nil {
				logger.Error("%p (%s): Error during the connect response payload creation: %s", clientConnection, clientId, err)
				messagePool.Release(connectTransporterMessage)
				clientConnection.internalClose()
				return
			}
			err = connectTransporterMessage.Write(clientConnection.connection)
			if err != nil {
				logger.Error("%p (%s): Error during the connect response payload sending: %s", clientConnection, clientId, err)
				messagePool.Release(connectTransporterMessage)
				clientConnection.internalClose()
			}
			logger.Info("%p (%s): Client connection established", clientConnection, clientId)

			//TODO: Register the client in the room controller

		case protocol.CommandReconnect:
			//TODO: This feature it not implemented yet!
		}
	}()
}

func (cc *ClientConnection) Send(message *protocol.TransporterMessage) error {
	err := message.Write(cc.connection)
	if err != nil {
		return err
	}
	return nil
}

func (cc *ClientConnection) Close() error {
	cc.internalClose()
	return nil
}

func (clientConnection *ClientConnection) SendErrorResponse(command uint32, errorCode int, errorMessage string) error {
	pool := clientConnection.owner.transporterMessagePool
	transportMessage := pool.Obtain()
	defer pool.Release(transportMessage)
	transportMessage.SetErrorResponseCommand(command)
	err := transportMessage.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	})
	if err != nil {
		return err
	}
	err = transportMessage.Write(clientConnection.connection)
	return err
}

func (clientConnection *ClientConnection) SendRoomCreateResponse(roomId string) error {
	pool := clientConnection.owner.transporterMessagePool
	message := pool.Obtain()
	defer pool.Release(message)
	message.SetResponseCommand(protocol.CommandCreateRoom)
	err := message.SetPayloadCreateRoomResponse(&protocol.TransporterMessagePayloadCreateRoomResponse{
		RoomId: roomId,
	})
	if err != nil {
		return err
	}
	err = message.Write(clientConnection.connection)
	return err
}

func (clientConnection *ClientConnection) SendJoinRoomRequest(roomId string, clientId string) error {
	pool := clientConnection.owner.transporterMessagePool
	message := pool.Obtain()
	defer pool.Release(message)
	message.SetDirectCommand(protocol.CommandConnectRoom)
	err := message.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{
		RoomId:   roomId,
		ClientId: clientId,
	})
	if err != nil {
		return err
	}
	err = message.Write(clientConnection.connection)
	return err
}

func (clientConnection *ClientConnection) SendJoinRoomResponse(isAccepted int) error {
	pool := clientConnection.owner.transporterMessagePool
	message := pool.Obtain()
	defer pool.Release(message)

	message.SetDirectCommand(protocol.CommandConnectRoomResult)
	err := message.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{
		Accepted: isAccepted,
	})
	if err != nil {
		return err
	}
	err = message.Write(clientConnection.connection)
	return err
}

func (clientConnection *ClientConnection) handleProtocolMismatchError(clientProtocolVersion uint32) {
	message := clientConnection.owner.transporterMessagePool.Obtain()
	defer clientConnection.owner.transporterMessagePool.Release(message)
	log.Printf("Error: Protocol vertsion not supported, transporter: %d, client: %d\n", protocol.ProtocolVersion, clientProtocolVersion)
	message.SetErrorResponseCommand(protocol.CommandConnect)
	err := message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    protocol.ErrorProtocolNotSupported,
		ErrorMessage: fmt.Sprintf("Protocol version mismatch, transporter: %d, client: %d", protocol.ProtocolVersion, clientProtocolVersion),
	})
	if err != nil {
		log.Println("Error during the error payload creation", err)
	} else if err := message.Write(clientConnection.connection); err != nil {
		log.Println("Error during the message sending to the client: ", err)
	}
	clientConnection.owner.internalCloseClient(clientConnection)
}

func (clientConnection *ClientConnection) handleConnectResponse() error {
	message := clientConnection.owner.transporterMessagePool.Obtain()
	defer clientConnection.owner.transporterMessagePool.Release(message)

	message.SetResponseCommand(protocol.CommandConnect)
	err := message.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{
		ClientId: utils.GenerateClientId(),
	})

	if err != nil {
		log.Println("Error during the connect response payload creation: ", err)
		clientConnection.internalClose()
		return err
	}

	if err := message.Write(clientConnection.connection); err != nil {
		log.Println("Error during the connect response payload sending: ", err)
		clientConnection.internalClose()
		return err
	}
	return nil
}

func (clientConnection *ClientConnection) internalClose() {
	clientConnection.owner.internalCloseClient(clientConnection)
}
