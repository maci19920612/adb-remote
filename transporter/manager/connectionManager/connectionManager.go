package connectionManager

import (
	"adb-remote.maci.team/shared"
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
	transporterMessagePool *shared.TransportMessagePool
	waitGroup              *sync.WaitGroup
	config                 *config.AppConfiguration
	server                 *net.Listener
	connections            *list.List
	context                *context.Context
	cancelFunc             *context.CancelFunc
	Logger                 *slog.Logger
}

type IConnectionManager interface {
	StartServer() error
}

func CreateConnectionManager(config *config.AppConfiguration, logger *slog.Logger) *ConnectionManager {
	ctx, cancelFunc := context.WithCancel(context.Background())
	return &ConnectionManager{
		config:                 config,
		transporterMessagePool: shared.CreateTransporterMessagePool(),
		waitGroup:              new(sync.WaitGroup),
		connections:            list.New(),
		context:                &ctx,
		cancelFunc:             &cancelFunc,
		Logger:                 logger,
	}
}

func (si *ConnectionManager) StartServer() error {
	si.Logger.Info("StartServer called")
	serverAddress := si.config.TransporterAddress
	serverType := si.config.TransporterType
	server, err := net.Listen(serverType, serverAddress)
	if err != nil {
		return err
	}
	si.server = &server

	si.waitGroup.Add(1)
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
				connection: &connection,
				owner:      si,
			}
			si.connections.PushFront(&clientConnection)
		}
	}()
	si.waitGroup.Wait()

	return nil
}

func (serverInstance *ConnectionManager) internalCloseClient(clientConnection *ClientConnection) {
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
