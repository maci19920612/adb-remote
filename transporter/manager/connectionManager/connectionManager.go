package connectionManager

import (
	"adb-remote.maci.team/shared"
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/config"
	"container/list"
	"context"
	"log"
	"log/slog"
	"net"
	"sync"
)

const ConnectionPoolSize = 10 //TODO: Move this into configuration

type ClientMessageContainer struct {
	Sender  *ClientConnection
	Message *protocol.TransporterMessage
}

type ConnectionManager struct {
	transporterMessagePool    *shared.TransportMessagePool
	waitGroup                 *sync.WaitGroup
	config                    *config.TransporterConfiguration
	server                    *net.Listener
	connections               *list.List
	context                   *context.Context
	cancelFunc                *context.CancelFunc
	logger                    *slog.Logger
	ClientDisconnectedChannel chan *ClientConnection
	ClientMessageChannel      chan *ClientMessageContainer
}

func CreateConnectionManager(config *config.TransporterConfiguration, logger *slog.Logger) *ConnectionManager {
	ctx, cancelFunc := context.WithCancel(context.Background())
	return &ConnectionManager{
		config:                    config,
		transporterMessagePool:    shared.CreateTransporterMessagePool(),
		waitGroup:                 new(sync.WaitGroup),
		connections:               list.New(),
		context:                   &ctx,
		cancelFunc:                &cancelFunc,
		logger:                    logger,
		ClientDisconnectedChannel: make(chan *ClientConnection, ConnectionPoolSize),
		ClientMessageChannel:      make(chan *ClientMessageContainer, ConnectionPoolSize),
	}
}

func (cm *ConnectionManager) StartServer() error {
	logger := cm.logger
	logger.Info("Starting the transporter server")
	server, err := net.Listen("tcp", cm.config.Address)
	if err != nil {
		logger.Error("Transporter server can't be created: %s", err)
		return err
	}
	cm.server = &server

	cm.waitGroup.Add(1)
	go func() {
		logger.Info("Transporter server listening")
	listenerLoop:
		for {
			connection, err := server.Accept()
			if opError, ok := err.(*net.OpError); ok && opError.Op == "accept" {

				logger.Error("Transporter server accept OP error: %s", err)
				break listenerLoop
			} else if err != nil {
				logger.Error("Transporter server accept GENERAL error: ", err)
				connection.Close()
				break listenerLoop
			}

			clientConnection := ClientConnection{
				connection:  &connection,
				owner:       cm,
				isConnected: true,
			}
			clientConnection.start()
			cm.connections.PushFront(&clientConnection)
		}
	}()
	cm.waitGroup.Wait()

	return nil
}

func (cm *ConnectionManager) internalCloseClient(clientConnection *ClientConnection) {
	var currentElement *list.Element = nil

	//Close the TCP connection
	err := (*clientConnection.connection).Close()
	if err != nil {
		log.Println("Error during the client connection close, silently failed")
	}

	//Cancel the tasks

	//Remove the connection reference from the server instance
	connections := cm.connections
	for currentElement = connections.Front(); currentElement != nil; currentElement = currentElement.Next() {
		if currentElement.Value == clientConnection {
			break
		}
	}

	if currentElement == nil {
		log.Println("Client connection not registered, can't be closed")
		return
	}
	cm.ClientDisconnectedChannel <- clientConnection
	connections.Remove(currentElement)
}
