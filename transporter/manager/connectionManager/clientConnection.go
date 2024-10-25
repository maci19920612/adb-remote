package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/utils"
	"fmt"
	"io"
	"log"
	"net"
)

type ClientConnection struct {
	connection  *net.Conn
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
	connectionManager := cc.owner
	logger := cc.owner.logger
	messagePool := cc.owner.transporterMessagePool
	logger.Info("Client connection started", "clientReference", fmt.Sprintf("%p", cc))
	//logger.Info(fmt.Sprintf("%p (-): Client connection started", cc))
	go func() {
		connectTransporterMessage := messagePool.Obtain()
		err := connectTransporterMessage.Read(cc.connection)
		if err != nil {
			logger.Error(fmt.Sprintf("%p (-): Error during the transporter message reading: %s", cc, err))
			cc.internalClose()
			messagePool.Release(connectTransporterMessage)
			return
		}
		switch connectTransporterMessage.Command() {
		case protocol.CommandConnect:
			payload, err := connectTransporterMessage.GetPayloadConnect()
			if err != nil {
				logger.Error(fmt.Sprintf("%p (-): Error during the connect payload reading: %s", cc, err))
				cc.internalClose()
				messagePool.Release(connectTransporterMessage)
				return
			}
			//ERROR: Protocol version not supported
			if payload.ProtocolVersion != protocol.ProtocolVersion {
				logger.Error(fmt.Sprintf("%p (-): Protocol version mismatch: server=%d, client=%d", cc, protocol.ProtocolVersion, payload.ProtocolVersion))
				cc.handleProtocolMismatchError(payload.ProtocolVersion)
				messagePool.Release(connectTransporterMessage)
				return
			}
			logger.Info(fmt.Sprintf("%p (-): A client started the connection process", cc))
			clientId := utils.GenerateClientId()
			logger.Info(fmt.Sprintf("%p (%s): Client ID generated:", cc, clientId))
			cc.clientId = clientId
			connectTransporterMessage.SetResponseCommand(protocol.CommandConnect)
			err = connectTransporterMessage.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{
				ClientId: clientId,
			})
			if err != nil {
				logger.Error(fmt.Sprintf("%p (%s): Error during the connect response payload creation: %s", cc, clientId, err))
				messagePool.Release(connectTransporterMessage)
				cc.internalClose()
				return
			}
			err = connectTransporterMessage.Write(cc.connection)
			if err != nil {
				logger.Error(fmt.Sprintf("%p (%s): Error during the connect response payload sending: %s", cc, clientId, err))
				messagePool.Release(connectTransporterMessage)
				cc.internalClose()
			}
			logger.Info(fmt.Sprintf("%p (%s): Client connection established", cc, clientId))
			messagePool.Release(connectTransporterMessage)
		case protocol.CommandReconnect:
			logger.Info(fmt.Sprintf("%p (-): This feature is not implemented yet"))
			cc.internalClose()
		default:
			logger.Error(fmt.Sprintf("%p (-): Client tried an invalid handshake, close the connection quietly", cc))
			cc.internalClose()
			return
		}

		for {
			logger.Info(fmt.Sprintf("%p (%s): Waiting for message", cc, cc.GetClientId()))
			message := messagePool.Obtain()
			err := message.Read(cc.connection)
			logger.Info(fmt.Sprintf("%p (%s): Message received from the client: %x", cc, cc.GetClientId(), message.Command()))
			if err != nil {
				logger.Error(fmt.Sprintf("%p (%s): Invalid message read from the network: %s", cc, cc.GetClientId(), err))
				if err != io.EOF {
					message.SetErrorResponseCommand(message.Command())
					if err := message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
						ErrorCode:    protocol.ErrorUnknown,
						ErrorMessage: "Invalid message read from the network, close the connection",
					}); err == nil {
						_ = message.Write(cc.connection)
						cc.internalClose()
						return
					}
				} else {
					cc.internalClose()
					return
				}
			}
			logger.Info(fmt.Sprintf("%p (%s): Message processed, sending in the ClientMessageChannel", cc, cc.GetClientId()))
			//TODO: Use an object pool??
			connectionManager.ClientMessageChannel <- &ClientMessageContainer{
				Sender:  cc,
				Message: message,
			}
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

func (cc *ClientConnection) SendErrorResponse(command uint32, errorCode int, errorMessage string) error {
	pool := cc.owner.transporterMessagePool
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
	err = transportMessage.Write(cc.connection)
	return err
}

func (cc *ClientConnection) SendRoomCreateResponse(roomId string) error {
	pool := cc.owner.transporterMessagePool
	message := pool.Obtain()
	defer pool.Release(message)
	message.SetResponseCommand(protocol.CommandCreateRoom)
	err := message.SetPayloadCreateRoomResponse(&protocol.TransporterMessagePayloadCreateRoomResponse{
		RoomId: roomId,
	})
	if err != nil {
		return err
	}
	err = message.Write(cc.connection)
	return err
}

func (cc *ClientConnection) SendJoinRoomRequest(roomId string, clientId string) error {
	pool := cc.owner.transporterMessagePool
	message := pool.Obtain()
	defer pool.Release(message)
	message.SetDirectCommand(protocol.CommandJoinRoom)
	err := message.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{
		RoomId:   roomId,
		ClientId: clientId,
	})
	if err != nil {
		return err
	}
	err = message.Write(cc.connection)
	return err
}

func (cc *ClientConnection) SendJoinRoomResponse(isAccepted int) error {
	pool := cc.owner.transporterMessagePool
	message := pool.Obtain()
	defer pool.Release(message)
	message.SetResponseCommand(protocol.CommandJoinRoom)
	err := message.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{
		Accepted: isAccepted,
	})
	if err != nil {
		return err
	}
	err = message.Write(cc.connection)
	return err
}

func (cc *ClientConnection) SendInvalidPayloadError(command uint32) error {
	pool := cc.owner.transporterMessagePool
	message := pool.Obtain()
	defer pool.Release(message)
	message.SetErrorResponseCommand(command)
	err := message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorMessage: "Invalid command payload",
		ErrorCode:    protocol.ErrorInvalidPayload,
	})
	if err != nil {
		return err
	}
	return message.Write(cc.connection)
}

func (cc *ClientConnection) handleProtocolMismatchError(clientProtocolVersion uint32) {
	message := cc.owner.transporterMessagePool.Obtain()
	defer cc.owner.transporterMessagePool.Release(message)
	log.Printf("Error: Protocol vertsion not supported, transporter: %d, client: %d\n", protocol.ProtocolVersion, clientProtocolVersion)
	message.SetErrorResponseCommand(protocol.CommandConnect)
	err := message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    protocol.ErrorProtocolNotSupported,
		ErrorMessage: fmt.Sprintf("Protocol version mismatch, transporter: %d, client: %d", protocol.ProtocolVersion, clientProtocolVersion),
	})
	if err != nil {
		log.Println("Error during the error payload creation", err)
	} else if err := message.Write(cc.connection); err != nil {
		log.Println("Error during the message sending to the client: ", err)
	}
	cc.owner.internalCloseClient(cc)
}

func (cc *ClientConnection) handleConnectResponse() error {
	message := cc.owner.transporterMessagePool.Obtain()
	defer cc.owner.transporterMessagePool.Release(message)

	message.SetResponseCommand(protocol.CommandConnect)
	err := message.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{
		ClientId: utils.GenerateClientId(),
	})

	if err != nil {
		log.Println("Error during the connect response payload creation: ", err)
		cc.internalClose()
		return err
	}

	if err := message.Write(cc.connection); err != nil {
		log.Println("Error during the connect response payload sending: ", err)
		cc.internalClose()
		return err
	}
	return nil
}
