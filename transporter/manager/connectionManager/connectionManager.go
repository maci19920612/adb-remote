package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/shared/utils"
	"adb-remote.maci.team/transporter/config"
	"adb-remote.maci.team/transporter/tlsutil"
	"container/list"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

const ConnectionPoolSize = 10 //TODO: Move this into configuration

// MessageContainer is the pooled, disposable handle delivered for every
// message a ClientConnection reads. The consumer must Dispose it once done.
type MessageContainer = utils.DisposableObjectContainer[protocol.TransporterMessage]

type ClientMessageContainer struct {
	Sender  *ClientConnection
	Message *MessageContainer
}

type ConnectionManager struct {
	transporterMessagePool    *utils.ObjectPool[protocol.TransporterMessage]
	waitGroup                 *sync.WaitGroup
	config                    *config.TransporterConfiguration
	server                    net.Listener
	connections               *list.List
	mutex                     *sync.Mutex
	context                   context.Context
	cancelFunc                context.CancelFunc
	logger                    *slog.Logger
	ClientDisconnectedChannel chan *ClientConnection
	ClientMessageChannel      chan *ClientMessageContainer
}

func CreateConnectionManager(config *config.TransporterConfiguration, logger *slog.Logger) *ConnectionManager {
	ctx, cancelFunc := context.WithCancel(context.Background())
	transporterMessageFactory := func() *protocol.TransporterMessage {
		return protocol.CreateTransporterMessage()
	}
	return &ConnectionManager{
		config:                    config,
		transporterMessagePool:    utils.NewObjectPool(transporterMessageFactory),
		waitGroup:                 new(sync.WaitGroup),
		connections:               list.New(),
		mutex:                     new(sync.Mutex),
		context:                   ctx,
		cancelFunc:                cancelFunc,
		logger:                    logger,
		ClientDisconnectedChannel: make(chan *ClientConnection, ConnectionPoolSize),
		ClientMessageChannel:      make(chan *ClientMessageContainer, ConnectionPoolSize),
	}
}

// StartServer starts listening (over TLS, using a self-signed certificate
// generated on first run and persisted thereafter — see transporter/tlsutil)
// and blocks until the server is stopped via Stop or the listener fails
// permanently.
func (cm *ConnectionManager) StartServer() error {
	logger := cm.logger
	logger.Info("Starting the transporter server")

	cert, err := tlsutil.LoadOrCreateCertificate(cm.config.CertPath(), cm.config.KeyPath())
	if err != nil {
		logger.Error(fmt.Sprintf("Transporter TLS certificate could not be loaded/created: %s", err))
		return err
	}
	if fingerprint, err := tlsutil.Fingerprint(cert); err == nil {
		logger.Info(fmt.Sprintf("Transporter TLS certificate fingerprint: %s", fingerprint))
	}

	server, err := tls.Listen("tcp", cm.config.Address, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		logger.Error(fmt.Sprintf("Transporter server can't be created: %s", err))
		return err
	}
	cm.mutex.Lock()
	cm.server = server
	cm.mutex.Unlock()

	cm.waitGroup.Add(1)
	go func() {
		defer cm.waitGroup.Done()
		logger.Info("Transporter server listening")
		for {
			connection, err := server.Accept()
			if err != nil {
				if cm.context.Err() != nil {
					logger.Info("Transporter server stopped, exiting the accept loop")
					return
				}
				logger.Error(fmt.Sprintf("Transporter server accept error: %s", err))
				continue
			}

			clientConnection := &ClientConnection{
				connection: connection,
				owner:      cm,
			}
			cm.registerConnection(clientConnection)
			clientConnection.start()
		}
	}()
	cm.waitGroup.Wait()

	return nil
}

// Stop closes the listener and every registered client connection, causing
// StartServer to return.
func (cm *ConnectionManager) Stop() {
	cm.cancelFunc()

	cm.mutex.Lock()
	server := cm.server
	connections := make([]*ClientConnection, 0, cm.connections.Len())
	for element := cm.connections.Front(); element != nil; element = element.Next() {
		connections = append(connections, element.Value.(*ClientConnection))
	}
	cm.mutex.Unlock()

	if server != nil {
		_ = server.Close()
	}
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (cm *ConnectionManager) registerConnection(clientConnection *ClientConnection) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.connections.PushFront(clientConnection)
}

func (cm *ConnectionManager) internalCloseClient(clientConnection *ClientConnection) {
	err := clientConnection.connection.Close()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		cm.logger.Warn(fmt.Sprintf("Error during the client connection close, silently ignored: %s", err))
	}

	cm.mutex.Lock()
	var target *list.Element
	for element := cm.connections.Front(); element != nil; element = element.Next() {
		if element.Value == clientConnection {
			target = element
			break
		}
	}
	if target != nil {
		cm.connections.Remove(target)
	}
	cm.mutex.Unlock()

	if target == nil {
		cm.logger.Warn("Client connection not registered, can't be closed")
		return
	}
	cm.ClientDisconnectedChannel <- clientConnection
}
