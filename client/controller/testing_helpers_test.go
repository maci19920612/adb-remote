package controller

import (
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/internal/testtls"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fakeTransporter(t *testing.T) (address string, connections <-chan net.Conn) {
	t.Helper()
	listener := testtls.Listen(t)

	ch := make(chan net.Conn, 1)
	go func() {
		conn, err := testtls.Accept(listener)
		if err != nil {
			return
		}
		ch <- conn
	}()
	return listener.Addr().String(), ch
}

// newConnectedClient starts a real transportLayer.Client against a fake
// transporter server and returns both the client and the server-side
// connection so the test can script the transporter's responses.
func newConnectedClient(t *testing.T) (*transportLayer.Client, net.Conn) {
	t.Helper()
	address, connections := fakeTransporter(t)
	client, err := transportLayer.CreateClient(newTestLogger(), &config.ClientConfiguration{TransporterAddress: address})
	if err != nil {
		t.Fatalf("CreateClient failed: %s", err)
	}
	if err := client.Start(); err != nil {
		t.Fatalf("Start failed: %s", err)
	}
	t.Cleanup(client.Close)

	select {
	case serverConn := <-connections:
		t.Cleanup(func() { _ = serverConn.Close() })
		return client, serverConn
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the client to connect")
		return nil, nil
	}
}

func readMessage(t *testing.T, conn net.Conn) *protocol.TransporterMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	message := protocol.CreateTransporterMessage()
	if err := message.Read(conn); err != nil {
		t.Fatalf("failed to read a message: %s", err)
	}
	return message
}
