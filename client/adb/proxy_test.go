package adb

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startTestProxy(t *testing.T, port string, roomId string) *AdbProxy {
	t.Helper()
	proxy := NewAdbProxy(port, newTestLogger()).(*AdbProxy)
	if err := proxy.Start(roomId); err != nil {
		t.Fatalf("Start failed: %s", err)
	}
	t.Cleanup(proxy.Stop)
	return proxy
}

func freeLocalPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %s", err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	return port
}

func TestProxyCompletesHandshakeAndYieldsConnection(t *testing.T) {
	port := freeLocalPort(t)
	proxy := startTestProxy(t, port, "ROOM42")

	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("failed to dial the proxy: %s", err)
	}
	defer conn.Close()

	request := CreateMessage()
	if err := request.Set(CommandConnect, 1, MaxPayloadLength, []byte("host::")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := request.Write(conn); err != nil {
		t.Fatalf("failed to write the CNXN request: %s", err)
	}

	response := CreateMessage()
	if err := response.Read(conn); err != nil {
		t.Fatalf("failed to read the CNXN response: %s", err)
	}
	if response.Command() != CommandConnect {
		t.Fatalf("expected a CNXN response, got %x", response.Command())
	}
	banner := response.DataString()
	if !strings.Contains(banner, "ROOM42") {
		t.Fatalf("expected the banner to mention the room id, got %q", banner)
	}
	if !strings.Contains(banner, "features=") || !strings.Contains(banner, "shell_v2") {
		t.Fatalf("expected the banner to advertise shell_v2 (without it, real adb clients silently lose exit codes), got %q", banner)
	}

	select {
	case handshaked := <-proxy.Connections():
		if handshaked == nil {
			t.Fatalf("expected a non-nil connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the handshaked connection")
	}
}

func TestProxyClosesConnectionOnUnexpectedCommand(t *testing.T) {
	port := freeLocalPort(t)
	proxy := startTestProxy(t, port, "ROOM1")

	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("failed to dial the proxy: %s", err)
	}
	defer conn.Close()

	request := CreateMessage()
	if err := request.Set(CommandOkay, 0, 0, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := request.Write(conn); err != nil {
		t.Fatalf("failed to write the request: %s", err)
	}

	select {
	case <-proxy.Connections():
		t.Fatalf("did not expect a handshaked connection for a non-CNXN request")
	case <-time.After(200 * time.Millisecond):
	}

	buffer := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(buffer); err != io.EOF {
		t.Fatalf("expected the proxy to close the connection, got err=%v", err)
	}
}

func TestProxyStopClosesListener(t *testing.T) {
	port := freeLocalPort(t)
	proxy := NewAdbProxy(port, newTestLogger()).(*AdbProxy)
	if err := proxy.Start("ROOM1"); err != nil {
		t.Fatalf("Start failed: %s", err)
	}
	proxy.Stop()

	if _, err := net.Dial("tcp", "127.0.0.1:"+port); err == nil {
		t.Fatalf("expected dialing a stopped proxy to fail")
	}
}
