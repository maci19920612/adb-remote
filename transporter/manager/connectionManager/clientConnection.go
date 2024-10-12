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
}

func (clientConnection *ClientConnection) start() {
	go func() {
		messagePool := clientConnection.owner.transporterMessagePool
		connectTransporterMessage := messagePool.Obtain()
		err := connectTransporterMessage.Read(clientConnection.connection)
		if err != nil {
			log.Println("Error during the connection message read, close the client connection")
			clientConnection.internalClose()
			messagePool.Release(connectTransporterMessage)
			return
		}
		switch connectTransporterMessage.Command() {
		case protocol.CommandConnect:
			payload, err := connectTransporterMessage.GetPayloadConnect()
			if err != nil {
				clientConnection.internalClose()
				messagePool.Release(connectTransporterMessage)
				return
			}
			//ERROR: Protocol version not supported
			if payload.ProtocolVersion != protocol.ProtocolVersion {
				clientConnection.handleProtocolMismatchError(payload.ProtocolVersion)
				messagePool.Release(connectTransporterMessage)
				return
			}

			log.Println("Client connected")
			//TODO: Register the client in the room controller

		case protocol.CommandReconnect:
			//TODO: This feature it not implemented yet!
		}
	}()
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
