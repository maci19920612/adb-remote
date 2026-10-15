// Package relay pumps ADB protocol messages between a local net.Conn
// (either a real device reached through the smart socket, or a local ADB
// server that connected to our AdbProxy) and the transporter Client, so
// that two adb-remote clients can transparently forward an ADB session for
// each other.
package relay

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
)

// TransportClient is the subset of transportLayer.Client the relay depends
// on, allowing tests to substitute a fake implementation.
type TransportClient interface {
	SendAdbMessage(message *adb.AdbMessage) error
	Messages() <-chan *transportLayer.MessageContainer
}

var ErrTransportClosed = errors.New("the transporter message channel was closed")

// Run pumps ADB messages between conn and client until either side closes
// or errors, or ctx is cancelled, then closes conn and returns the reason
// the relay stopped.
//
// The peer (the room owner) is not trusted with the local adb-server: only
// stream traffic (OKAY, WRTE, CLSE) and OPENs for the reverse forwards the
// local adb-server requested itself are passed to it. adb-server aborts on
// an OPEN for anything else, which would take every other device the guest
// uses down with it.
func Run(ctx context.Context, conn net.Conn, client TransportClient, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	forwards := newReverseForwards()
	errChannel := make(chan error, 2)
	go func() { errChannel <- pumpLocalToRemote(ctx, conn, client, forwards) }()
	go func() { errChannel <- pumpRemoteToLocal(ctx, conn, client, forwards, logger) }()

	err := <-errChannel
	cancel()
	_ = conn.Close()
	<-errChannel

	return err
}

// pumpLocalToRemote reads ADB messages arriving on the local connection and
// forwards them to the peer through the transporter client, noting the
// reverse forwards the local adb-server requests on the way.
func pumpLocalToRemote(ctx context.Context, conn net.Conn, client TransportClient, forwards *reverseForwards) error {
	message := adb.CreateMessage()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := message.Read(conn); err != nil {
			return err
		}
		if message.Command() == adb.CommandOpen {
			forwards.observe(strings.TrimRight(message.DataString(), "\x00"))
		}
		if err := client.SendAdbMessage(message); err != nil {
			return err
		}
	}
}

// pumpRemoteToLocal reads TransporterMessages carrying an embedded ADB
// message and writes the decoded ADB message to the local connection.
func pumpRemoteToLocal(ctx context.Context, conn net.Conn, client TransportClient, forwards *reverseForwards, logger *slog.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case container, ok := <-client.Messages():
			if !ok {
				return ErrTransportClosed
			}
			if err := handleIncoming(conn, client, forwards, container, logger); err != nil {
				return err
			}
		}
	}
}

func handleIncoming(conn net.Conn, client TransportClient, forwards *reverseForwards, container *transportLayer.MessageContainer, logger *slog.Logger) error {
	message, err := container.Data()
	if err != nil {
		return err
	}
	if message.Command() != protocol.CommandAdbTransport {
		logger.Info(fmt.Sprintf("Ignoring unexpected message during relay, command: %x", message.Command()))
		return container.Dispose()
	}
	adbMessage, err := adb.DecodeMessage(message.Payload())
	if err != nil {
		logger.Error(fmt.Sprintf("Invalid ADB message received from the peer: %s", err))
		return container.Dispose()
	}
	switch adbMessage.Command() {
	case adb.CommandOkay, adb.CommandWrite, adb.CommandClose:
	case adb.CommandOpen:
		destination := strings.TrimRight(adbMessage.DataString(), "\x00")
		if !forwards.allows(destination) {
			logger.Warn(fmt.Sprintf("Refusing the peer's stream to %q, which isn't a reverse forward the local adb-server requested", destination))
			peerId := adbMessage.Arg1()
			if disposeErr := container.Dispose(); disposeErr != nil {
				return disposeErr
			}
			return refuseOpen(client, peerId)
		}
	default:
		logger.Warn(fmt.Sprintf("Ignoring an ADB %s message from the peer, which only belongs to the local connection", adbMessage.CommandString()))
		return container.Dispose()
	}
	writeErr := adbMessage.Write(conn)
	// The container (and the buffer adbMessage aliases) must not be
	// released back to the pool until the write has fully completed,
	// otherwise a concurrent Obtain() could hand the same memory out and
	// corrupt the in-flight write.
	if disposeErr := container.Dispose(); disposeErr != nil && writeErr == nil {
		return disposeErr
	}
	return writeErr
}

// refuseOpen closes the peer's side of a stream it tried to open, the way
// adb-server itself answers an OPEN it can't serve.
func refuseOpen(client TransportClient, peerId uint32) error {
	message := adb.CreateMessage()
	if err := message.Set(adb.CommandClose, 0, peerId, nil); err != nil {
		return err
	}
	return client.SendAdbMessage(message)
}

// reverseForwards mirrors the reverse forwards the local adb-server tracks
// for this connection (see atransport::UpdateReverseConfig in adb's
// transport.cpp): the local end of each, keyed by the device-side end. It
// is updated from the requests the local adb-server sends, and consulted
// for the OPENs the peer sends.
type reverseForwards struct {
	mu     sync.Mutex
	locals map[string]string
}

func newReverseForwards() *reverseForwards {
	return &reverseForwards{locals: make(map[string]string)}
}

// observe updates the forwards from a service the local adb-server opened,
// exactly as adb-server does when it sends the same request.
func (f *reverseForwards) observe(service string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(service, reverseForwardPrefix):
		spec := strings.TrimPrefix(service, reverseForwardPrefix)
		norebind := strings.HasPrefix(spec, reverseNoRebindPrefix)
		remote, local, ok := strings.Cut(strings.TrimPrefix(spec, reverseNoRebindPrefix), ";")
		if !ok {
			return
		}
		if _, exists := f.locals[remote]; exists && norebind {
			// The device refuses it, so adb-server doesn't record it.
			return
		}
		f.locals[remote] = local
	case strings.HasPrefix(service, reverseKillForwardPrefix):
		remote := strings.TrimPrefix(service, reverseKillForwardPrefix)
		if !strings.Contains(remote, ";") {
			delete(f.locals, remote)
		}
	case service == reverseKillForwardAll:
		clear(f.locals)
	}
}

// allows reports whether destination is the local end of a reverse forward,
// i.e. whether the local adb-server accepts the device opening it.
func (f *reverseForwards) allows(destination string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, local := range f.locals {
		if local == destination {
			return true
		}
	}
	return false
}
