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

type ConnectionManager struct {
	transporterMessagePool      *shared.TransportMessagePool
	waitGroup                   *sync.WaitGroup
	config                      *config.AppConfiguration
	server                      *net.Listener
	connections                 *list.List
	context                     *context.Context
	cancelFunc                  *context.CancelFunc
	logger                      *slog.Logger
	clientConnectedCallbacks    []OnClientConnectedCallback
	clientDisconnectedCallbacks []OnClientDisconnectedCallback
	clientMessageCallbacks      []OnClientMessage
}

type OnClientConnectedCallback func(connection *ClientConnection)
type OnClientDisconnectedCallback func(connection *ClientConnection)
type OnClientMessage func(sender *ClientConnection, message *protocol.TransporterMessage)

type IConnectionManager interface {
	StartServer() error
	RegisterOnClientConnectedCallback(callback OnClientConnectedCallback)
	RegisterOnClientDisconnectedCallback(callback OnClientDisconnectedCallback)
	RegisterOnClientMessageCallback(callback OnClientMessage)
}

func CreateConnectionManager(config *config.AppConfiguration, logger *slog.Logger) *ConnectionManager {
	ctx, cancelFunc := context.WithCancel(context.Background())
	return &ConnectionManager{
		config:                      config,
		transporterMessagePool:      shared.CreateTransporterMessagePool(),
		waitGroup:                   new(sync.WaitGroup),
		connections:                 list.New(),
		context:                     &ctx,
		cancelFunc:                  &cancelFunc,
		logger:                      logger,
		clientConnectedCallbacks:    make([]OnClientConnectedCallback, 10),
		clientDisconnectedCallbacks: make([]OnClientDisconnectedCallback, 10),
		clientMessageCallbacks:      make([]OnClientMessage, 10),
	}
}

func (cm *ConnectionManager) StartServer() error {
	logger := cm.logger
	logger.Info("Starting the transporter server")
	serverAddress := cm.config.TransporterAddress
	serverType := cm.config.TransporterType
	server, err := net.Listen(serverType, serverAddress)
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
				connection: &connection,
				owner:      cm,
			}
			clientConnection.start()
			cm.connections.PushFront(&clientConnection)
		}
	}()
	cm.waitGroup.Wait()

	return nil
}

func (cm *ConnectionManager) RegisterOnClientConnectedCallback(callback OnClientConnectedCallback) {
	cm.clientConnectedCallbacks = append(cm.clientConnectedCallbacks, callback)
}
func (cm *ConnectionManager) RegisterOnClientDisconnectedCallback(callback OnClientDisconnectedCallback) {
	cm.clientDisconnectedCallbacks = append(cm.clientDisconnectedCallbacks, callback)
}
func (cm *ConnectionManager) RegisterOnClientMessageCallback(callback OnClientMessage) {
	cm.clientMessageCallbacks = append(cm.clientMessageCallbacks, callback)
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
	connections.Remove(currentElement)
}
