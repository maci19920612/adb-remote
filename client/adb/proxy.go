package adb

import (
	"context"
	"fmt"
	"log/slog"
	"net"
)

// DefaultProxyPort is the local TCP port a guest's AdbProxy listens on by
// default, for the local ADB server to "adb connect" to.
const DefaultProxyPort = "5038"

// IAdbProxy listens locally for a real ADB server to "adb connect" to,
// performs the ADB CNXN handshake on its behalf (pretending to be a single
// device named after the room), and hands the now-handshaked connection off
// through Connections for a relay to pump ADB protocol messages over the
// transporter.
type IAdbProxy interface {
	// Start begins listening for local ADB connections, presenting itself
	// as a device named after roomId once a CNXN handshake completes.
	Start(roomId string) error
	// Stop closes the listener and any pending accept loop. Already
	// handshaked connections handed out through Connections are left open;
	// the caller owns their lifecycle from that point on.
	Stop()
	// Connections yields one net.Conn per successfully handshaked local
	// ADB connection.
	Connections() <-chan net.Conn
}

type AdbProxy struct {
	port       string
	listener   net.Listener
	cancelFunc context.CancelFunc

	connections chan net.Conn

	//Dependencies
	logger *slog.Logger
}

func NewAdbProxy(port string, logger *slog.Logger) IAdbProxy {
	return &AdbProxy{
		port:        port,
		connections: make(chan net.Conn),
		logger:      logger,
	}
}

func (p *AdbProxy) Connections() <-chan net.Conn {
	return p.connections
}

func (p *AdbProxy) Start(roomId string) error {
	logger := p.logger
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%s", p.port))
	if err != nil {
		return err
	}
	p.listener = listener
	ctx, cancelFunc := context.WithCancel(context.Background())
	p.cancelFunc = cancelFunc

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					logger.Info("AdbProxy stopped, exiting the connection accept loop")
					return
				}
				logger.Error(fmt.Sprintf("Error during the connection accept: %s", err))
				continue
			}
			logger.Info("Accepted a new local connection, starting the CNXN handshake")
			go p.handleConnection(ctx, conn, roomId)
		}
	}()
	return nil
}

func (p *AdbProxy) handleConnection(ctx context.Context, conn net.Conn, roomId string) {
	logger := p.logger
	message := CreateMessage()
	if err := message.Read(conn); err != nil {
		logger.Error(fmt.Sprintf("Invalid ADB message read from the network: %s", err))
		_ = conn.Close()
		return
	}
	if command := message.Command(); command != CommandConnect {
		logger.Info(fmt.Sprintf("Unexpected command from the local ADB instance, expected CNXN: %x", command))
		_ = conn.Close()
		return
	}
	protocolVersion := message.Arg1()
	peerMaxMessageSize := message.Arg2()
	logger.Info(fmt.Sprintf("Protocol version: %d, peer max message size: %d", protocolVersion, peerMaxMessageSize))
	// Each side of a CNXN handshake independently advertises its own
	// MAXDATA; there is no requirement that they match. We always
	// advertise our own capacity here regardless of what the peer offered
	// (real adb clients commonly offer up to 1MiB, far more than our
	// fixed-size buffers hold).
	if err := message.Set(CommandConnect, protocolVersion, MaxPayloadLength, []byte(fmt.Sprintf("device:wrapper-remote-%s", roomId))); err != nil {
		logger.Error(fmt.Sprintf("Failed to build the CNXN response: %s", err))
		_ = conn.Close()
		return
	}
	if err := message.Write(conn); err != nil {
		logger.Error(fmt.Sprintf("Error during the CNXN adb response sending: %s", err))
		_ = conn.Close()
		return
	}

	select {
	case p.connections <- conn:
	case <-ctx.Done():
		_ = conn.Close()
	}
}

func (p *AdbProxy) Stop() {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
	}
}
