package connectionManager

import (
	"adb-remote.maci.team/shared/certfingerprint"
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/config"
	"bytes"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	return startTunedTestServer(t, configure, func(*ConnectionManager) {})
}

// startTunedTestServer is startTestServerWith that also lets tune adjust
// the manager itself (e.g. shorten its timeouts) before it starts.
func startTunedTestServer(t *testing.T, configure func(*config.TransporterConfiguration), tune func(*ConnectionManager)) (*ConnectionManager, string) {
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
	tune(cm)

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

func dialAndHandshake(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	performHandshake(t, conn)
	return conn
}

func registeredConnections(cm *ConnectionManager) []*ClientConnection {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	connections := make([]*ClientConnection, 0, cm.connections.Len())
	for element := cm.connections.Front(); element != nil; element = element.Next() {
		connections = append(connections, element.Value.(*ClientConnection))
	}
	return connections
}

// waitForConnectionCount waits until exactly count connections are
// registered, e.g. for the readiness probe of startTestServer to go away.
func waitForConnectionCount(t *testing.T, cm *ConnectionManager, count int) []*ClientConnection {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		connections := registeredConnections(cm)
		if len(connections) == count {
			return connections
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d registered connections, got %d", count, len(connections))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expectClosedWithin fails unless the server closes conn within limit.
func expectClosedWithin(t *testing.T, conn net.Conn, limit time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(limit))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("expected the server to close the connection within %s, got %s", limit, err)
	}
}

// TestSendGuestLeftCarriesNoPayload is a regression test for GUEST_LEFT
// leaking whatever a pooled message last carried — possibly another room's
// ADB traffic — since it never set a payload of its own.
func TestSendGuestLeftCarriesNoPayload(t *testing.T) {
	cm, address := startTestServer(t)
	waitForConnectionCount(t, cm, 0)
	conn := dialAndHandshake(t, address)
	owner := waitForConnectionCount(t, cm, 1)[0]

	container := cm.transporterMessagePool.Obtain()
	stale, err := container.Data()
	if err != nil {
		t.Fatalf("Data() failed: %s", err)
	}
	if err := stale.SetRawPayload([]byte("another room's adb traffic")); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	_ = container.Dispose()

	if err := owner.SendGuestLeft(); err != nil {
		t.Fatalf("SendGuestLeft failed: %s", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	message := protocol.CreateTransporterMessage()
	if err := message.Read(conn); err != nil {
		t.Fatalf("failed to read GUEST_LEFT: %s", err)
	}
	if message.Command() != protocol.CommandGuestLeft || message.PayloadLength() != 0 {
		t.Fatalf("expected an empty GUEST_LEFT, got command %x with %d payload bytes: %q", message.Command(), message.PayloadLength(), message.Payload())
	}
}

// TestSilentClientIsDisconnected covers a client that connects and never
// completes the TLS handshake or the CNXN exchange: it must not hold a
// connection open forever.
func TestSilentClientIsDisconnected(t *testing.T) {
	_, address := startTunedTestServer(t, func(*config.TransporterConfiguration) {}, func(cm *ConnectionManager) {
		cm.handshakeTimeout = 200 * time.Millisecond
	})

	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	defer conn.Close()

	expectClosedWithin(t, conn, 3*time.Second)
}

// TestTrickledMessageIsDisconnected covers a client that starts a message
// and then stalls: once a message has begun, the rest of it has to arrive
// in time.
func TestTrickledMessageIsDisconnected(t *testing.T) {
	_, address := startTunedTestServer(t, func(*config.TransporterConfiguration) {}, func(cm *ConnectionManager) {
		cm.messageReadTimeout = 200 * time.Millisecond
	})
	conn := dialAndHandshake(t, address)

	header := make([]byte, protocol.HeaderSize)
	protocol.ByteOrder.PutUint32(header[0:4], protocol.CommandCreateRoom)
	protocol.ByteOrder.PutUint32(header[4:8], 100)
	if _, err := conn.Write(append(header, make([]byte, 10)...)); err != nil {
		t.Fatalf("failed to write the partial message: %s", err)
	}

	expectClosedWithin(t, conn, 3*time.Second)
}

// TestIdleClientStaysConnected guards the other side of the read timeout:
// a client may sit idle between messages for as long as it likes (an owner
// waiting for a guest, a guest between adb commands).
func TestIdleClientStaysConnected(t *testing.T) {
	cm, address := startTunedTestServer(t, func(*config.TransporterConfiguration) {}, func(cm *ConnectionManager) {
		cm.messageReadTimeout = 100 * time.Millisecond
	})
	conn := dialAndHandshake(t, address)

	time.Sleep(400 * time.Millisecond)
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandCreateRoom)
	if err := request.Write(conn); err != nil {
		t.Fatalf("failed to write after idling: %s", err)
	}

	select {
	case container := <-cm.ClientMessageChannel:
		_ = container.Message.Dispose()
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the idle client's message to arrive")
	}
}

func TestConnectionsPerIPAreLimited(t *testing.T) {
	cm, address := startTestServerWith(t, func(c *config.TransporterConfiguration) {
		c.MaxConnectionsPerIP = 2
	})
	waitForConnectionCount(t, cm, 0)
	dialAndHandshake(t, address)
	dialAndHandshake(t, address)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err == nil {
		defer conn.Close()
		expectClosedWithin(t, conn, 2*time.Second)
	}
}

func TestConnectionsAreLimitedInTotal(t *testing.T) {
	cm, address := startTestServerWith(t, func(c *config.TransporterConfiguration) {
		c.MaxConnections = 1
	})
	waitForConnectionCount(t, cm, 0)
	dialAndHandshake(t, address)

	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err == nil {
		defer conn.Close()
		expectClosedWithin(t, conn, 2*time.Second)
	}
}

// TestClosingDoesNotBlockOnUndrainedDisconnects is a regression test for a
// self-deadlock: the room manager closes connections from its own
// goroutine, and is also the only reader of ClientDisconnectedChannel, so
// Close must not wait for room on that channel.
func TestClosingDoesNotBlockOnUndrainedDisconnects(t *testing.T) {
	cm, address := startTestServer(t)
	waitForConnectionCount(t, cm, 0)
	count := cap(cm.ClientDisconnectedChannel) + 1
	for i := 0; i < count; i++ {
		dialAndHandshake(t, address)
	}
	connections := waitForConnectionCount(t, cm, count)

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatalf("Close blocked on a full ClientDisconnectedChannel")
	}
}

func TestTLS12IsRefused(t *testing.T) {
	_, address := startTestServer(t)
	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12})
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected a TLS 1.2 handshake to be refused")
	}
}

// syncBuffer collects log output written from several goroutines.
type syncBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

// startLoggingServer starts a server on transporterConfig and returns what
// it logged by the time it accepts connections.
func startLoggingServer(t *testing.T, transporterConfig *config.TransporterConfiguration) string {
	t.Helper()
	logs := &syncBuffer{}
	cm := CreateConnectionManager(transporterConfig, slog.New(slog.NewTextHandler(logs, nil)))
	go func() { _ = cm.StartServer() }()
	defer cm.Stop()
	for i := 0; i < 100; i++ {
		if conn, err := net.DialTimeout("tcp", transporterConfig.Address, 50*time.Millisecond); err == nil {
			_ = conn.Close()
			return logs.String()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the server didn't start")
	return ""
}

// TestStartupPrintsTheCertificateFingerprintToPin checks the operator gets
// the exact client setting that pins the transporter's certificate, both
// when the certificate was just generated and when it already existed.
func TestStartupPrintsTheCertificateFingerprintToPin(t *testing.T) {
	dir := t.TempDir()
	transporterConfig := &config.TransporterConfiguration{
		Address:     freeLocalAddress(t),
		TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "key.pem"),
	}

	generated := startLoggingServer(t, transporterConfig)
	cert, err := tls.LoadX509KeyPair(transporterConfig.CertPath(), transporterConfig.KeyPath())
	if err != nil {
		t.Fatalf("failed to load the generated certificate: %s", err)
	}
	fingerprint := certfingerprint.Of(cert.Certificate[0])
	for _, expected := range []string{"Generated a new TLS certificate", fingerprint, "transporterCertificateFingerprint"} {
		if !strings.Contains(generated, expected) {
			t.Fatalf("expected the startup log to contain %q, got:\n%s", expected, generated)
		}
	}

	transporterConfig.Address = freeLocalAddress(t)
	loaded := startLoggingServer(t, transporterConfig)
	for _, expected := range []string{"Loaded the TLS certificate", fingerprint, "transporterCertificateFingerprint"} {
		if !strings.Contains(loaded, expected) {
			t.Fatalf("expected the startup log to contain %q, got:\n%s", expected, loaded)
		}
	}
}
