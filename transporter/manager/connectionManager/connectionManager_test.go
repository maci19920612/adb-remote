package connectionManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/config"
	"bytes"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"os"
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
	return startTestServerWith(t, func(*config.TransporterConfiguration) {})
}

// startTestServerWith starts a server whose configuration configure may
// adjust (e.g. to turn on debug verbosity) before it starts.
func startTestServerWith(t *testing.T, configure func(*config.TransporterConfiguration)) (*ConnectionManager, string) {
	t.Helper()
	address := freeLocalAddress(t)
	dir := t.TempDir()
	transporterConfig := &config.TransporterConfiguration{
		Address:     address,
		TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "key.pem"),
	}
	configure(transporterConfig)
	cm := CreateConnectionManager(transporterConfig, newTestLogger())

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

// startDebugTestServer starts a server with debug verbosity, capturing to a
// file whose path it returns alongside the usual server handles.
func startDebugTestServer(t *testing.T) (*ConnectionManager, string, string) {
	t.Helper()
	pcapPath := filepath.Join(t.TempDir(), "transporter.pcapng")
	cm, address := startTestServerWith(t, func(c *config.TransporterConfiguration) {
		c.Verbosity = config.VerbosityDebug
		c.PcapFile = pcapPath
	})
	return cm, address, pcapPath
}

func readCapture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read the capture file: %s", err)
	}
	return data
}

func waitForServerClose(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("expected the server to close the connection, got %s", err)
	}
}

func TestDebugVerbosityCapturesTrafficFromNonTLSClients(t *testing.T) {
	cm, address, pcapPath := startDebugTestServer(t)

	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("HELLO NOT TLS\r\n\r\n")); err != nil {
		t.Fatalf("failed to write: %s", err)
	}
	waitForServerClose(t, conn)
	cm.Stop()

	if !bytes.Contains(readCapture(t, pcapPath), []byte("HELLO NOT TLS")) {
		t.Fatalf("expected the plaintext bytes of the failed TLS handshake in the capture")
	}
}

func TestDebugVerbosityCapturesTLSHandshakeAndSecrets(t *testing.T) {
	cm, address, pcapPath := startDebugTestServer(t)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()
	performHandshake(t, conn)
	cm.Stop()

	capture := readCapture(t, pcapPath)
	if !containsClientHello(capture) {
		t.Fatalf("expected the raw TLS ClientHello in the capture")
	}
	if !bytes.Contains(capture, []byte("CLIENT_TRAFFIC_SECRET_0 ")) {
		t.Fatalf("expected the TLS session secrets in the capture")
	}
}

func TestDefaultVerbosityDoesNotCapture(t *testing.T) {
	pcapPath := filepath.Join(t.TempDir(), "transporter.pcapng")
	cm, address := startTestServerWith(t, func(c *config.TransporterConfiguration) {
		c.PcapFile = pcapPath
	})

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()
	performHandshake(t, conn)
	cm.Stop()

	if _, err := os.Stat(pcapPath); !os.IsNotExist(err) {
		t.Fatalf("expected no capture file without debug verbosity, got err=%v", err)
	}
}

func TestStartServerFailsWhenTheCaptureCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	cm := CreateConnectionManager(&config.TransporterConfiguration{
		Address:     freeLocalAddress(t),
		TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "key.pem"),
		Verbosity:   config.VerbosityDebug,
		PcapFile:    filepath.Join(dir, "missing-dir", "transporter.pcapng"),
	}, newTestLogger())

	result := make(chan error, 1)
	go func() { result <- cm.StartServer() }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatalf("expected StartServer to fail when the capture file can't be created")
		}
	case <-time.After(2 * time.Second):
		cm.Stop()
		t.Fatalf("expected StartServer to fail when the capture file can't be created, but it started serving")
	}
}

// containsClientHello looks for a TLS handshake record header (content type
// 22, legacy version 3.x) whose first handshake message is a ClientHello
// (type 1).
func containsClientHello(data []byte) bool {
	for i := 0; i+5 < len(data); i++ {
		if data[i] == 0x16 && data[i+1] == 0x03 && data[i+5] == 0x01 {
			return true
		}
	}
	return false
}
