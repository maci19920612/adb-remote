package main

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/utils"
	"container/list"
	"context"
	"fmt"
	"log"
	"net"
	"sync"
)

type ServerConfiguration struct {
	ServerType                string
	ServerAddress             string
	ConnectionPoolInitialSize int
}

type ServerInstance struct {
	waitGroup   *sync.WaitGroup
	config      *ServerConfiguration
	server      *net.Listener
	connections *list.List
	context     *context.Context
	cancelFunc  *context.CancelFunc
}

type ClientConnection struct {
	connection *net.Conn
	//TODO: Create an object pool to acquire and release a transporterMessage
	transportMessage *protocol.TransporterMessage
}

func StartServer(config ServerConfiguration) (*ServerInstance, error) {
	server, err := net.Listen(config.ServerType, config.ServerAddress)
	if err != nil {
		return nil, err
	}
	context, cancelFunc := context.WithCancel(context.Background())
	serverInstance := ServerInstance{
		waitGroup:   new(sync.WaitGroup),
		config:      &config,
		server:      &server,
		connections: list.New(),
		context:     &context,
		cancelFunc:  &cancelFunc,
	}

	go func() {
	listenerLoop:
		for {
			connection, err := server.Accept()
			if opError, ok := err.(*net.OpError); ok && opError.Op == "accept" {
				log.Println("Accept error, server closed", err)
				break listenerLoop
			} else if err != nil {
				log.Println("Error during the 'accept' operation: ", err)
				connection.Close()
				break listenerLoop
			}
			clientConnection := ClientConnection{
				connection:       &connection,
				transportMessage: protocol.CreateTransporterMessage(),
			}
			serverInstance.connections.PushFront(&clientConnection)
		}
	}()

	return &serverInstance, nil
}

func (serverInstance *ServerInstance) internalCloseClient(clientConnection *ClientConnection) {
	var currentElement *list.Element = nil

	//Close the TCP connection
	err := (*clientConnection.connection).Close()
	if err != nil {
		log.Println("Error during the client connection close, silently failed")
	}

	//Cancel the tasks

	//Remove the connection reference from the server instance
	connections := serverInstance.connections
	for currentElement = connections.Front(); currentElement != nil; currentElement = currentElement.Next() {
		if currentElement.Value == clientConnection {
			break
		}
	}
	if currentElement == nil {
		log.Println("Client connection not registered, can't be closed")
		return
	}
	connections.Remove(currentElement)
}

func (clientConnection *ClientConnection) start(serverInstance *ServerInstance) {
	go func() {
		err := clientConnection.transportMessage.Read(clientConnection.connection)
		if err != nil {
			log.Println("Error during the connection message read, close the client connection")
			serverInstance.internalCloseClient(clientConnection)
			return
		}
		switch clientConnection.transportMessage.Command() {
		case protocol.CommandConnect:
			payload, err := clientConnection.transportMessage.GetPayloadConnect()
			if err != nil {
				serverInstance.internalCloseClient(clientConnection)
				return
			}
			//ERROR: Protocol version not supported
			if payload.ProtocolVersion != protocol.ProtocolVersion {
				clientConnection.handleProtocolMismatchError(payload.ProtocolVersion, serverInstance)
				return
			}
			clientConnection.transportMessage.SetResponseCommand(protocol.CommandConnect)
			err = clientConnection.transportMessage.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{
				ClientId: utils.GenerateClientId(),
			})
			if err != nil {
				log.Println("Error during the connect response payload creation: ", err)
				serverInstance.internalCloseClient(clientConnection)
				return
			}
			err = clientConnection.submitMessage()
			if err != nil {
				log.Println("Error during the connect response payload sending: ", err)
				serverInstance.internalCloseClient(clientConnection)
				return
			}
			log.Println("Client connected")
			//TODO: Register the client in the room controller

		case protocol.CommandReconnect:
			//TODO: This feature it not implemented yet!
		}
	}()
}

func (clientConnection *ClientConnection) handleProtocolMismatchError(clientProtocolVersion uint32, serverInstance *ServerInstance) {
	log.Printf("Error: Protocol vertsion not supported, transporter: %d, client: %d\n", protocol.ProtocolVersion, clientProtocolVersion)
	clientConnection.transportMessage.SetErrorResponseCommand(protocol.CommandConnect)
	err := clientConnection.transportMessage.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    protocol.ErrorProtocolNotSupported,
		ErrorMessage: fmt.Sprintf("Protocol version mismatch, transporter: %d, client: %d", protocol.ProtocolVersion, clientProtocolVersion),
	})
	if err != nil {
		log.Println("Error during the error payload creation", err)
	} else if err := clientConnection.submitMessage(); err != nil {
		log.Println("Error during the message sending to the client: ", err)
	}
	serverInstance.internalCloseClient(clientConnection)
}

func (clientConnection *ClientConnection) submitMessage() error {
	return clientConnection.transportMessage.Write(clientConnection.connection)
}
