package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/config"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func freeLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %s", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func startTestServer(t *testing.T) (*ConnectionManager, string) {
	t.Helper()
	address := freeLocalAddress(t)
	dir := t.TempDir()
	cm := CreateConnectionManager(&config.TransporterConfiguration{
		Address:     address,
		TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "key.pem"),
	}, newTestLogger())

	started := make(chan struct{})
	go func() {
		// StartServer blocks until Stop is called; poll until the listener
		// is actually accepting connections before signalling ready.
		go func() { _ = cm.StartServer() }()
		for i := 0; i < 100; i++ {
			if conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond); err == nil {
				_ = conn.Close()
				close(started)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(started)
	}()
	<-started
	t.Cleanup(cm.Stop)
	return cm, address
}

func performHandshake(t *testing.T, conn net.Conn) string {
	t.Helper()
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandConnect)
	if err := request.SetPayloadConnect(&protocol.TransporterMessagePayloadConnect{ProtocolVersion: protocol.ProtocolVersion}); err != nil {
		t.Fatalf("SetPayloadConnect failed: %s", err)
	}
	if err := request.Write(conn); err != nil {
		t.Fatalf("failed to write the CNXN request: %s", err)
	}

	response := protocol.CreateTransporterMessage()
	if err := response.Read(conn); err != nil {
		t.Fatalf("failed to read the CNXN response: %s", err)
	}
	if response.Command() != protocol.CommandConnect|protocol.CommandResponseMask {
		t.Fatalf("expected a CNXN response, got %x", response.Command())
	}
	payload, err := response.GetPayloadConnectResponse()
	if err != nil {
		t.Fatalf("GetPayloadConnectResponse failed: %s", err)
	}
	if payload.ClientId == "" {
		t.Fatalf("expected a non-empty client id")
	}
	return payload.ClientId
}

func TestHandshakeAssignsClientId(t *testing.T) {
	_, address := startTestServer(t)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()

	clientId := performHandshake(t, conn)
	if clientId == "" {
		t.Fatalf("expected a client id to be assigned")
	}
}

func TestHandshakeRejectsProtocolVersionMismatch(t *testing.T) {
	_, address := startTestServer(t)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()

	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandConnect)
	if err := request.SetPayloadConnect(&protocol.TransporterMessagePayloadConnect{ProtocolVersion: protocol.ProtocolVersion + 1}); err != nil {
		t.Fatalf("SetPayloadConnect failed: %s", err)
	}
	if err := request.Write(conn); err != nil {
		t.Fatalf("failed to write the CNXN request: %s", err)
	}

	response := protocol.CreateTransporterMessage()
	if err := response.Read(conn); err != nil {
		t.Fatalf("failed to read the response: %s", err)
	}
	if !response.IsError() {
		t.Fatalf("expected an error response for a protocol version mismatch")
	}
	payload, err := response.GetErrorPayload()
	if err != nil {
		t.Fatalf("GetErrorPayload failed: %s", err)
	}
	if payload.ErrorCode != protocol.ErrorProtocolNotSupported {
		t.Fatalf("expected error code %d, got %d", protocol.ErrorProtocolNotSupported, payload.ErrorCode)
	}
}

func TestHandshakeRejectsUnexpectedFirstCommand(t *testing.T) {
	_, address := startTestServer(t)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()

	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandCreateRoom)
	if err := request.Write(conn); err != nil {
		t.Fatalf("failed to write the request: %s", err)
	}

	buffer := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(buffer); err != io.EOF {
		t.Fatalf("expected the server to close the connection, got err=%v", err)
	}
}

func TestStopClosesActiveConnections(t *testing.T) {
	cm, address := startTestServer(t)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()
	performHandshake(t, conn)

	cm.Stop()

	buffer := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(buffer); err != io.EOF {
		t.Fatalf("expected the connection to be closed after Stop, got err=%v", err)
	}
}

func TestMessagesAfterHandshakeArriveOnClientMessageChannel(t *testing.T) {
	cm, address := startTestServer(t)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()
	performHandshake(t, conn)

	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandCreateRoom)
	if err := request.Write(conn); err != nil {
		t.Fatalf("failed to write the request: %s", err)
	}

	select {
	case container := <-cm.ClientMessageChannel:
		message, err := container.Message.Data()
		if err != nil {
			t.Fatalf("Data() failed: %s", err)
		}
		if message.Command() != protocol.CommandCreateRoom {
			t.Fatalf("expected command %x, got %x", protocol.CommandCreateRoom, message.Command())
		}
		_ = container.Message.Dispose()
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the message on ClientMessageChannel")
	}
}
