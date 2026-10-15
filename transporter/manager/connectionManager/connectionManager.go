package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/shared/utils"
	"adb-remote.maci.team/transporter/capture"
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
	"time"
)

const ConnectionPoolSize = 10 //TODO: Move this into configuration

// Default timeouts of a client connection. handshakeTimeout bounds the TLS
// handshake plus the CNXN exchange; messageReadTimeout bounds how long the
// rest of a message may take once its first byte arrived (a client may
// idle between messages for as long as it likes); writeTimeout bounds every
// single write, so a client that stops reading is disconnected instead of
// holding its writer up forever.
const (
	defaultHandshakeTimeout   = 10 * time.Second
	defaultMessageReadTimeout = 60 * time.Second
	defaultWriteTimeout       = 30 * time.Second
)

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
	capture                   *capture.Capture // nil unless debug verbosity is on
	connections               *list.List
	connectionsPerIP          map[string]int
	mutex                     *sync.Mutex
	context                   context.Context
	cancelFunc                context.CancelFunc
	logger                    *slog.Logger
	ClientDisconnectedChannel chan *ClientConnection
	ClientMessageChannel      chan *ClientMessageContainer

	handshakeTimeout   time.Duration
	messageReadTimeout time.Duration
	writeTimeout       time.Duration
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
		connectionsPerIP:          make(map[string]int),
		mutex:                     new(sync.Mutex),
		context:                   ctx,
		cancelFunc:                cancelFunc,
		logger:                    logger,
		ClientDisconnectedChannel: make(chan *ClientConnection, ConnectionPoolSize),
		ClientMessageChannel:      make(chan *ClientMessageContainer, ConnectionPoolSize),
		handshakeTimeout:          defaultHandshakeTimeout,
		messageReadTimeout:        defaultMessageReadTimeout,
		writeTimeout:              defaultWriteTimeout,
	}
}

// StartServer starts listening (over TLS 1.3, using a self-signed
// certificate generated on first run and persisted thereafter — see
// transporter/tlsutil) and blocks until the server is stopped via Stop or
// the listener fails permanently. With debug verbosity, every accepted
// connection is recorded below the TLS layer to the configured pcapng file
// (see transporter/capture).
func (cm *ConnectionManager) StartServer() error {
	logger := cm.logger
	logger.Info("Starting the transporter server")

	cert, created, err := tlsutil.LoadOrCreateCertificate(cm.config.CertPath(), cm.config.KeyPath())
	if err != nil {
		logger.Error(fmt.Sprintf("Transporter TLS certificate could not be loaded/created: %s", err))
		return err
	}
	if created {
		logger.Info(fmt.Sprintf("Generated a new TLS certificate at %s; clients that pinned a previous one need the new fingerprint below", cm.config.CertPath()))
	} else {
		logger.Info(fmt.Sprintf("Loaded the TLS certificate from %s", cm.config.CertPath()))
	}
	fingerprint, err := tlsutil.Fingerprint(cert)
	if err != nil {
		logger.Error(fmt.Sprintf("Transporter TLS certificate fingerprint could not be computed: %s", err))
		return err
	}
	logger.Info(fmt.Sprintf("Transporter TLS certificate fingerprint: %s", fingerprint))
	logger.Info(fmt.Sprintf(`To pin it, set "transporterCertificateFingerprint": "%s" in the clients' config.json`, fingerprint))

	// Every client is our own Go client, so there is no older peer to stay
	// compatible with.
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	listener, err := net.Listen("tcp", cm.config.Address)
	if err != nil {
		logger.Error(fmt.Sprintf("Transporter server can't be created: %s", err))
		return err
	}

	var packetCapture *capture.Capture
	if cm.config.IsDebug() {
		packetCapture, err = capture.Create(cm.config.PcapPath(), logger)
		if err != nil {
			logger.Error(fmt.Sprintf("Transporter packet capture can't be created: %s", err))
			_ = listener.Close()
			return err
		}
		logger.Warn(fmt.Sprintf("Debug verbosity: recording all traffic, including TLS secrets, to %s", cm.config.PcapPath()))
		// Wrapping the raw listener (rather than the TLS one) is what lets
		// the capture see failed handshakes and non-TLS clients too.
		listener = packetCapture.Listener(listener)
		tlsConfig.KeyLogWriter = packetCapture.KeyLogWriter()
	}

	server := tls.NewListener(listener, tlsConfig)
	cm.mutex.Lock()
	cm.server = server
	cm.capture = packetCapture
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

			clientConnection := newClientConnection(connection, cm)
			if !cm.registerConnection(clientConnection) {
				// The TLS handshake hasn't started yet, so this costs
				// nothing beyond the accept itself.
				_ = connection.Close()
				continue
			}
			clientConnection.start()
		}
	}()
	cm.waitGroup.Wait()

	return nil
}

// Stop closes the listener and every registered client connection, causing
// StartServer to return, and then the packet capture, if any.
func (cm *ConnectionManager) Stop() {
	cm.cancelFunc()

	cm.mutex.Lock()
	server := cm.server
	packetCapture := cm.capture
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
	// Closed last, so the connections closed above are still recorded.
	if packetCapture != nil {
		if err := packetCapture.Close(); err != nil {
			cm.logger.Warn(fmt.Sprintf("Error during the packet capture close, silently ignored: %s", err))
		}
	}
}

// registerConnection records a newly accepted connection, unless that would
// exceed the configured connection limits, in which case it reports false
// and the connection must be dropped.
func (cm *ConnectionManager) registerConnection(clientConnection *ClientConnection) bool {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	ip := clientConnection.RemoteIP()
	if cm.connections.Len() >= cm.config.ConnectionLimit() {
		cm.logger.Warn(fmt.Sprintf("Refusing a connection from %s: the transporter already has %d connections", ip, cm.connections.Len()))
		return false
	}
	if cm.connectionsPerIP[ip] >= cm.config.ConnectionLimitPerIP() {
		cm.logger.Warn(fmt.Sprintf("Refusing a connection from %s: that address already has %d connections", ip, cm.connectionsPerIP[ip]))
		return false
	}
	cm.connections.PushFront(clientConnection)
	cm.connectionsPerIP[ip]++
	return true
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
		ip := clientConnection.RemoteIP()
		cm.connectionsPerIP[ip]--
		if cm.connectionsPerIP[ip] <= 0 {
			delete(cm.connectionsPerIP, ip)
		}
	}
	cm.mutex.Unlock()

	if target == nil {
		cm.logger.Warn("Client connection not registered, can't be closed")
		return
	}
	cm.notifyDisconnected(clientConnection)
}

// notifyDisconnected reports clientConnection on ClientDisconnectedChannel
// without ever blocking the caller: the room manager closes connections
// from its own goroutine and is also the channel's only reader, so waiting
// for room on a full channel there would deadlock it.
func (cm *ConnectionManager) notifyDisconnected(clientConnection *ClientConnection) {
	select {
	case cm.ClientDisconnectedChannel <- clientConnection:
	default:
		go func() {
			select {
			case cm.ClientDisconnectedChannel <- clientConnection:
			case <-cm.context.Done():
			}
		}()
	}
}
