package relay

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

// ownerStream tracks one multiplexed ADB stream on the owner side: a single
// service invocation (e.g. one "adb shell" or "adb sync" session) relayed
// between the guest and a dedicated connection to the local adb-server.
//
// Fields follow the ADB wire convention that a message's arg1 is the
// sender's own id for the stream and arg2 is the id the sender believes the
// receiver uses for it (0 until the receiver has assigned one, i.e. before
// OPEN is acknowledged).
type ownerStream struct {
	guestId uint32 // the id the guest assigned this stream (arg1 in its OPEN)
	ownId   uint32 // the id we assigned this stream

	conn net.Conn
	// sendPermit is a buffered(1) token: present means we may send the
	// next WRTE to the guest. ADB requires a sender to wait for an OKAY
	// after each WRTE before sending another for the same stream; a
	// stream starts with one token available (the initial send does not
	// need to wait for anything beyond the OPEN/OKAY exchange itself).
	sendPermit chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
}

// OwnerMultiplexer implements the owner side of a shared-device room: for
// every OPEN the guest sends, it opens a fresh connection to the local
// adb-server for the requested service and relays that one stream's bytes,
// since real adb-server does not expose a raw device transport pass-through
// (see relay.go's package doc and the README for why).
//
// A single OwnerMultiplexer is meant to live for the whole room, across
// however many guest sessions come and go: Dispatch only consumes
// CommandAdbTransport messages, so it composes with a caller that also
// needs to handle other message types (e.g. room join requests) arriving
// on the same Client — see client/controller/ownerController.go.
type OwnerMultiplexer struct {
	smartSocket adb.IAdbSmartSocket
	deviceId    string
	client      TransportClient
	logger      *slog.Logger

	nextId uint32 // atomic; monotonically increasing, never reused

	mu      sync.Mutex
	streams map[uint32]*ownerStream
}

func NewOwnerMultiplexer(smartSocket adb.IAdbSmartSocket, deviceId string, client TransportClient, logger *slog.Logger) *OwnerMultiplexer {
	return &OwnerMultiplexer{
		smartSocket: smartSocket,
		deviceId:    deviceId,
		client:      client,
		logger:      logger,
		streams:     make(map[uint32]*ownerStream),
	}
}

// RunOwner is a convenience wrapper around OwnerMultiplexer for callers
// that don't need to interleave other message handling: it owns the read
// loop over client.Messages() itself, dispatching every CommandAdbTransport
// message and logging/ignoring anything else. It blocks until ctx is
// cancelled or the transporter connection is lost, and closes every
// still-open stream before returning.
func RunOwner(ctx context.Context, smartSocket adb.IAdbSmartSocket, deviceId string, client TransportClient, logger *slog.Logger) error {
	m := NewOwnerMultiplexer(smartSocket, deviceId, client, logger)
	defer m.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case container, ok := <-client.Messages():
			if !ok {
				return ErrTransportClosed
			}
			message, err := container.Data()
			if err != nil {
				_ = container.Dispose()
				return err
			}
			if message.Command() != protocol.CommandAdbTransport {
				logger.Info(fmt.Sprintf("Ignoring unexpected message during relay, command: %x", message.Command()))
				_ = container.Dispose()
				continue
			}
			m.Dispatch(container)
		}
	}
}

// Dispatch decodes container as an embedded ADB message and routes it to
// the appropriate stream, creating one for an OPEN. The caller must only
// pass containers whose Command() is CommandAdbTransport; Dispatch always
// disposes of container before returning.
func (m *OwnerMultiplexer) Dispatch(container *transportLayer.MessageContainer) {
	defer func() { _ = container.Dispose() }()

	message, err := container.Data()
	if err != nil {
		m.logger.Error(fmt.Sprintf("Unusable pooled message container: %s", err))
		return
	}
	adbMessage, err := adb.DecodeMessage(message.Payload())
	if err != nil {
		m.logger.Error(fmt.Sprintf("Invalid ADB message received from the guest: %s", err))
		return
	}

	switch adbMessage.Command() {
	case adb.CommandOpen:
		m.handleOpen(adbMessage.Arg1(), adbMessage.DataString())
	case adb.CommandWrite:
		m.handleWrite(adbMessage.Arg1(), adbMessage.Arg2(), adbMessage.Data())
	case adb.CommandOkay:
		m.handleOkay(adbMessage.Arg2())
	case adb.CommandClose:
		m.handleClose(adbMessage.Arg2())
	default:
		m.logger.Info(fmt.Sprintf("Ignoring unexpected ADB command during relay: %x", adbMessage.Command()))
	}
}

// Close closes every currently open stream.
func (m *OwnerMultiplexer) Close() {
	m.closeAllStreams()
}

// handleOpen services a new stream request: guestId is the id the guest
// picked for it, and rawService is the OPEN payload, a NUL-terminated
// smartsocket service string (e.g. "shell,v2,raw:echo hi\x00").
func (m *OwnerMultiplexer) handleOpen(guestId uint32, rawService string) {
	service := strings.TrimRight(rawService, "\x00")
	logger := m.logger
	logger.Info(fmt.Sprintf("Guest opened a stream (id=%d): %s", guestId, service))

	conn, err := m.smartSocket.OpenStream(m.deviceId, service)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to open service %q on the local device: %s", service, err))
		if sendErr := m.sendClose(0, guestId); sendErr != nil {
			logger.Error(fmt.Sprintf("Failed to notify the guest of the open failure: %s", sendErr))
		}
		return
	}

	stream := &ownerStream{
		guestId:    guestId,
		ownId:      atomic.AddUint32(&m.nextId, 1),
		conn:       conn,
		sendPermit: make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	stream.sendPermit <- struct{}{}

	m.mu.Lock()
	m.streams[stream.ownId] = stream
	m.mu.Unlock()

	if err := m.sendOkay(stream.ownId, guestId); err != nil {
		logger.Error(fmt.Sprintf("Failed to acknowledge opening stream %d: %s", stream.ownId, err))
		m.closeStream(stream, false)
		return
	}

	go m.pumpDeviceToGuest(stream)
}

func (m *OwnerMultiplexer) handleWrite(guestId uint32, ownId uint32, data []byte) {
	stream := m.lookup(ownId)
	if stream == nil {
		m.logger.Info(fmt.Sprintf("Received WRTE for an unknown or already-closed stream: %d", ownId))
		return
	}
	if _, err := stream.conn.Write(data); err != nil {
		m.logger.Error(fmt.Sprintf("Failed to write to the local device stream %d: %s", ownId, err))
		m.closeStream(stream, true)
		return
	}
	if err := m.sendOkay(ownId, guestId); err != nil {
		m.logger.Error(fmt.Sprintf("Failed to acknowledge a WRTE for stream %d: %s", ownId, err))
		m.closeStream(stream, false)
	}
}

func (m *OwnerMultiplexer) handleOkay(ownId uint32) {
	stream := m.lookup(ownId)
	if stream == nil {
		return
	}
	select {
	case stream.sendPermit <- struct{}{}:
	default:
		// A token is already available; real adb-server does not send
		// redundant OKAYs, but tolerate it rather than blocking.
	}
}

func (m *OwnerMultiplexer) handleClose(ownId uint32) {
	stream := m.lookup(ownId)
	if stream == nil {
		return
	}
	m.closeStream(stream, false)
}

func (m *OwnerMultiplexer) lookup(ownId uint32) *ownerStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams[ownId]
}

// pumpDeviceToGuest relays bytes read from the local device stream to the
// guest as WRTE messages, respecting the stream's flow-control token.
func (m *OwnerMultiplexer) pumpDeviceToGuest(stream *ownerStream) {
	buffer := make([]byte, adb.MaxPayloadLength)
	for {
		n, readErr := stream.conn.Read(buffer)
		if n > 0 {
			select {
			case <-stream.sendPermit:
			case <-stream.done:
				return
			}
			if err := m.sendWrite(stream.ownId, stream.guestId, buffer[:n]); err != nil {
				m.logger.Error(fmt.Sprintf("Failed to relay device output for stream %d: %s", stream.ownId, err))
				m.closeStream(stream, false)
				return
			}
		}
		if readErr != nil {
			m.closeStream(stream, true)
			return
		}
	}
}

// closeStream idempotently tears a stream down: closes its device
// connection, unregisters it, and (if notifyGuest) tells the guest it
// closed. It is safe to call from multiple goroutines for the same stream
// (e.g. a guest CLSE racing a local read error).
func (m *OwnerMultiplexer) closeStream(stream *ownerStream, notifyGuest bool) {
	stream.closeOnce.Do(func() {
		close(stream.done)
		_ = stream.conn.Close()

		m.mu.Lock()
		delete(m.streams, stream.ownId)
		m.mu.Unlock()

		if notifyGuest {
			if err := m.sendClose(stream.ownId, stream.guestId); err != nil {
				m.logger.Error(fmt.Sprintf("Failed to notify the guest that stream %d closed: %s", stream.ownId, err))
			}
		}
	})
}

func (m *OwnerMultiplexer) closeAllStreams() {
	m.mu.Lock()
	streams := make([]*ownerStream, 0, len(m.streams))
	for _, stream := range m.streams {
		streams = append(streams, stream)
	}
	m.mu.Unlock()

	for _, stream := range streams {
		m.closeStream(stream, false)
	}
}

func (m *OwnerMultiplexer) sendOkay(ownId uint32, guestId uint32) error {
	message := adb.CreateMessage()
	if err := message.Set(adb.CommandOkay, ownId, guestId, nil); err != nil {
		return err
	}
	return m.client.SendAdbMessage(message)
}

func (m *OwnerMultiplexer) sendWrite(ownId uint32, guestId uint32, data []byte) error {
	message := adb.CreateMessage()
	if err := message.Set(adb.CommandWrite, ownId, guestId, data); err != nil {
		return err
	}
	return m.client.SendAdbMessage(message)
}

func (m *OwnerMultiplexer) sendClose(ownId uint32, guestId uint32) error {
	message := adb.CreateMessage()
	if err := message.Set(adb.CommandClose, ownId, guestId, nil); err != nil {
		return err
	}
	return m.client.SendAdbMessage(message)
}
