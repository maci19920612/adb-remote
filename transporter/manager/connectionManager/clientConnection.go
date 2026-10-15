package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/utils"
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// outboundQueueSize bounds how many messages may wait to be written to one
// client. ADB flow control allows only one unacknowledged WRTE per stream,
// so a client that reads at all keeps this nearly empty; one that lets it
// fill up is not reading, and gets disconnected rather than stalling the
// room manager that feeds every client.
const outboundQueueSize = 64

var (
	ErrConnectionClosed  = errors.New("the client connection is closed")
	ErrOutboundQueueFull = errors.New("the client isn't reading its messages, its outbound queue is full")
	errUnusableContainer = errors.New("unusable pooled message container")
)

type ClientConnection struct {
	connection net.Conn
	reader     *bufio.Reader
	remoteIP   string
	owner      *ConnectionManager
	clientId   string
	closeOnce  sync.Once
	closed     chan struct{}

	// queueMutex guards queueClosed together with sends on outbound, so
	// nothing is queued once the writer has stopped draining it.
	queueMutex  sync.Mutex
	queueClosed bool
	outbound    chan *MessageContainer
}

func newClientConnection(connection net.Conn, owner *ConnectionManager) *ClientConnection {
	remoteIP, _, err := net.SplitHostPort(connection.RemoteAddr().String())
	if err != nil {
		remoteIP = connection.RemoteAddr().String()
	}
	return &ClientConnection{
		connection: connection,
		// Reading through a small buffer lets the read loop wait for the
		// next message with Peek, before it takes a pooled message buffer
		// for it; larger reads bypass the buffer.
		reader:   bufio.NewReaderSize(connection, 16),
		remoteIP: remoteIP,
		owner:    owner,
		closed:   make(chan struct{}),
		outbound: make(chan *MessageContainer, outboundQueueSize),
	}
}

// internalClose is called from the connection's own read and write loops,
// from the room manager and from ConnectionManager.Stop; sync.Once keeps
// the actual close/unregister logic from running more than once no matter
// which caller wins the race.
func (cc *ClientConnection) internalClose() {
	cc.closeOnce.Do(func() {
		cc.queueMutex.Lock()
		cc.queueClosed = true
		cc.queueMutex.Unlock()
		close(cc.closed)
		cc.owner.internalCloseClient(cc)
	})
}

func (cc *ClientConnection) GetClientId() string {
	return cc.clientId
}

// RemoteIP returns the address the client connected from, for the
// per-address limits.
func (cc *ClientConnection) RemoteIP() string {
	return cc.remoteIP
}

// IsClosed reports whether the connection was closed, so the room manager
// can ignore messages it read before that.
func (cc *ClientConnection) IsClosed() bool {
	select {
	case <-cc.closed:
		return true
	default:
		return false
	}
}

func (cc *ClientConnection) start() {
	go cc.writeLoop()
	go cc.run()
}

func (cc *ClientConnection) run() {
	logger := cc.owner.logger
	// An unrecovered panic on any goroutine terminates the whole process,
	// not just this connection's — a bug triggered by one malformed message
	// from one client would otherwise crash the transporter for every other
	// room and user. Recovering here downgrades that to "this one
	// connection gets closed", the same outcome as any other malformed
	// message.
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Sprintf("%p (%s): recovered from a panic, closing the connection: %v", cc, cc.GetClientId(), r))
			cc.internalClose()
		}
	}()

	logger.Info(fmt.Sprintf("%p (-): Client connection started from %s", cc, cc.connection.RemoteAddr()))

	if !cc.performHandshake() {
		return
	}

	for {
		if !cc.readNextMessage() {
			return
		}
	}
}

// performHandshake consumes the initial CNXN message and replies with the
// assigned client id. It returns false if the connection was closed as part
// of handling the handshake (either because it failed, or because the
// caller's protocol version was rejected). The TLS handshake happens on the
// first read, so the deadline covers it too.
func (cc *ClientConnection) performHandshake() bool {
	logger := cc.owner.logger
	pool := cc.owner.transporterMessagePool

	if err := cc.connection.SetReadDeadline(time.Now().Add(cc.owner.handshakeTimeout)); err != nil {
		cc.internalClose()
		return false
	}

	container := pool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		logger.Error(fmt.Sprintf("%p (-): Unusable pooled message container: %s", cc, err))
		cc.internalClose()
		return false
	}

	if err := message.Read(cc.reader); err != nil {
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

	err = cc.send(func(message *protocol.TransporterMessage) error {
		message.SetResponseCommand(protocol.CommandConnect)
		return message.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{
			ClientId: clientId,
		})
	})
	if err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Error during the connect response sending: %s", cc, clientId, err))
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

	// Idling between messages is fine; only once a message has started does
	// the rest of it have to arrive in time.
	if err := cc.connection.SetReadDeadline(time.Time{}); err != nil {
		cc.internalClose()
		return false
	}
	if _, err := cc.reader.Peek(1); err != nil {
		cc.logReadError(err)
		cc.internalClose()
		return false
	}
	if err := cc.connection.SetReadDeadline(time.Now().Add(cc.owner.messageReadTimeout)); err != nil {
		cc.internalClose()
		return false
	}

	container := pool.Obtain()
	message, err := container.Data()
	if err != nil {
		logger.Error(fmt.Sprintf("%p (%s): Unusable pooled message container: %s", cc, cc.GetClientId(), err))
		_ = container.Dispose()
		cc.internalClose()
		return false
	}

	if err := message.Read(cc.reader); err != nil {
		cc.logReadError(err)
		_ = container.Dispose()
		cc.internalClose()
		return false
	}

	select {
	case cc.owner.ClientMessageChannel <- &ClientMessageContainer{
		Sender:  cc,
		Message: container,
	}:
		// Ownership of the container passes to whoever consumes
		// ClientMessageChannel; they are responsible for disposing of it.
		return true
	case <-cc.closed:
		_ = container.Dispose()
		return false
	}
}

func (cc *ClientConnection) logReadError(err error) {
	logger := cc.owner.logger
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		logger.Info(fmt.Sprintf("%p (%s): Client disconnected", cc, cc.GetClientId()))
	} else {
		logger.Error(fmt.Sprintf("%p (%s): Invalid message read from the network: %s", cc, cc.GetClientId(), err))
	}
}

// writeLoop writes every queued message, in order, until the connection
// closes; a nil entry asks it to close the connection once everything
// queued before it has been written.
func (cc *ClientConnection) writeLoop() {
	logger := cc.owner.logger
	for {
		select {
		case <-cc.closed:
			cc.drainOutbound()
			return
		case container := <-cc.outbound:
			if container == nil {
				cc.internalClose()
				continue
			}
			err := cc.write(container)
			_ = container.Dispose()
			if err != nil {
				logger.Error(fmt.Sprintf("%p (%s): Failed to write to the client, closing the connection: %s", cc, cc.GetClientId(), err))
				cc.internalClose()
			}
		}
	}
}

func (cc *ClientConnection) write(container *MessageContainer) error {
	message, err := container.Data()
	if err != nil {
		return err
	}
	if err := cc.connection.SetWriteDeadline(time.Now().Add(cc.owner.writeTimeout)); err != nil {
		return err
	}
	_, err = cc.connection.Write(message.Bytes())
	return err
}

func (cc *ClientConnection) drainOutbound() {
	for {
		select {
		case container := <-cc.outbound:
			if container != nil {
				_ = container.Dispose()
			}
		default:
			return
		}
	}
}

// enqueue hands container (or nil, for a close once flushed) to the
// writer, which disposes it once written; on failure enqueue disposes it
// itself. It never blocks: a client whose queue is full isn't reading what
// it is sent, and is disconnected rather than allowed to hold up the room
// manager, which serves every room from one goroutine.
func (cc *ClientConnection) enqueue(container *MessageContainer) error {
	cc.queueMutex.Lock()
	err := ErrConnectionClosed
	if !cc.queueClosed {
		select {
		case cc.outbound <- container:
			err = nil
		default:
			err = ErrOutboundQueueFull
		}
	}
	cc.queueMutex.Unlock()

	if err == nil {
		return nil
	}
	if container != nil {
		_ = container.Dispose()
	}
	if errors.Is(err, ErrOutboundQueueFull) {
		cc.owner.logger.Warn(fmt.Sprintf("%p (%s): Client isn't reading its messages, closing the connection", cc, cc.GetClientId()))
		cc.internalClose()
	}
	return err
}

// send fills a pooled message with fill and queues it for writing.
func (cc *ClientConnection) send(fill func(message *protocol.TransporterMessage) error) error {
	container := cc.owner.transporterMessagePool.Obtain()
	message, err := container.Data()
	if err != nil {
		_ = container.Dispose()
		return errUnusableContainer
	}
	if err := fill(message); err != nil {
		_ = container.Dispose()
		return err
	}
	return cc.enqueue(container)
}

// Forward queues a message another client sent, to relay it to this one
// unchanged. The caller keeps its own reference to container and still
// has to dispose it.
func (cc *ClientConnection) Forward(container *MessageContainer) error {
	if err := container.UpdateCounter(2); err != nil {
		return err
	}
	return cc.enqueue(container)
}

func (cc *ClientConnection) Close() error {
	cc.internalClose()
	return nil
}

// CloseAfterFlush closes the connection once every message queued so far
// has been written, e.g. right after the reply that explains why.
func (cc *ClientConnection) CloseAfterFlush() {
	if err := cc.enqueue(nil); err != nil {
		cc.internalClose()
	}
}

func (cc *ClientConnection) SendErrorResponse(command uint32, errorCode int, errorMessage string) error {
	return cc.send(func(message *protocol.TransporterMessage) error {
		message.SetErrorResponseCommand(command)
		return message.SetErrorPayload(&protocol.TransporterMessagePayloadError{
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		})
	})
}

func (cc *ClientConnection) SendRoomCreateResponse(roomId string) error {
	return cc.send(func(message *protocol.TransporterMessage) error {
		message.SetResponseCommand(protocol.CommandCreateRoom)
		return message.SetPayloadCreateRoomResponse(&protocol.TransporterMessagePayloadCreateRoomResponse{
			RoomId: roomId,
		})
	})
}

// SendJoinRoomRequest forwards a guest's join request to this (owner)
// connection; request.ClientId must already identify the guest.
func (cc *ClientConnection) SendJoinRoomRequest(request *protocol.TransporterMessagePayloadConnectRoom) error {
	return cc.send(func(message *protocol.TransporterMessage) error {
		message.SetDirectCommand(protocol.CommandJoinRoom)
		return message.SetPayloadConnectRoom(request)
	})
}

// SendJoinRoomResponse forwards the room owner's accept/decline decision to
// this (guest) connection; result.ClientId and result.PublicKey must
// already identify the owner (see client/identity), so the guest can
// display the owner's fingerprint for out-of-band verification.
func (cc *ClientConnection) SendJoinRoomResponse(result *protocol.TransporterMessagePayloadConnectRoomResult) error {
	return cc.send(func(message *protocol.TransporterMessage) error {
		message.SetResponseCommand(protocol.CommandJoinRoom)
		return message.SetPayloadConnectRoomResult(result)
	})
}

// SendGuestLeft notifies this (owner) connection that its guest disconnected
// from the room, since the owner's own connection is otherwise unaffected
// and has no other way to learn about it.
func (cc *ClientConnection) SendGuestLeft() error {
	return cc.send(func(message *protocol.TransporterMessage) error {
		message.SetDirectCommand(protocol.CommandGuestLeft)
		message.ClearPayload()
		return nil
	})
}

func (cc *ClientConnection) SendInvalidPayloadError(command uint32) error {
	return cc.SendErrorResponse(command, protocol.ErrorInvalidPayload, "Invalid command payload")
}

func (cc *ClientConnection) handleProtocolMismatchError(clientProtocolVersion uint32) {
	logger := cc.owner.logger
	logger.Error(fmt.Sprintf("Protocol version not supported, transporter: %d, client: %d", protocol.ProtocolVersion, clientProtocolVersion))
	err := cc.SendErrorResponse(
		protocol.CommandConnect,
		protocol.ErrorProtocolNotSupported,
		fmt.Sprintf("Protocol version mismatch, transporter: %d, client: %d", protocol.ProtocolVersion, clientProtocolVersion),
	)
	if err != nil {
		logger.Error(fmt.Sprintf("Error during the protocol mismatch error sending: %s", err))
	}
	cc.CloseAfterFlush()
}
