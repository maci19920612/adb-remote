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
func Run(ctx context.Context, conn net.Conn, client TransportClient, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errChannel := make(chan error, 2)
	go func() { errChannel <- pumpLocalToRemote(ctx, conn, client) }()
	go func() { errChannel <- pumpRemoteToLocal(ctx, conn, client, logger) }()

	err := <-errChannel
	cancel()
	_ = conn.Close()
	<-errChannel

	return err
}

// pumpLocalToRemote reads ADB messages arriving on the local connection and
// forwards them to the peer through the transporter client.
func pumpLocalToRemote(ctx context.Context, conn net.Conn, client TransportClient) error {
	message := adb.CreateMessage()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := message.Read(conn); err != nil {
			return err
		}
		if err := client.SendAdbMessage(message); err != nil {
			return err
		}
	}
}

// pumpRemoteToLocal reads TransporterMessages carrying an embedded ADB
// message and writes the decoded ADB message to the local connection.
func pumpRemoteToLocal(ctx context.Context, conn net.Conn, client TransportClient, logger *slog.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case container, ok := <-client.Messages():
			if !ok {
				return ErrTransportClosed
			}
			if err := handleIncoming(conn, container, logger); err != nil {
				return err
			}
		}
	}
}

func handleIncoming(conn net.Conn, container *transportLayer.MessageContainer, logger *slog.Logger) error {
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
