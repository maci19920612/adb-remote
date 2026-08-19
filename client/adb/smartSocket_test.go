package adb

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAdbServer emulates the subset of the ADB "smart socket" host protocol
// used by AdbSmartSocket: a 4-hex-digit length prefix, followed by a
// command, followed by an OKAY/FAIL status and an optional
// length-prefixed response body.
func fakeAdbServer(t *testing.T, handle func(command string, conn net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start the fake adb server: %s", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		lengthBuffer := make([]byte, 4)
		if _, err := io.ReadFull(conn, lengthBuffer); err != nil {
			return
		}
		var length int
		if _, err := fmt.Sscanf(string(lengthBuffer), "%04x", &length); err != nil {
			return
		}
		commandBuffer := make([]byte, length)
		if _, err := io.ReadFull(conn, commandBuffer); err != nil {
			return
		}
		handle(string(commandBuffer), conn)
	}()

	return listener.Addr().String()
}

func writeSmartSocketResponse(conn net.Conn, body string) {
	_, _ = conn.Write([]byte("OKAY"))
	_, _ = conn.Write([]byte(fmt.Sprintf("%04x", len(body))))
	_, _ = conn.Write([]byte(body))
}

// readSmartSocketCommand is safe to call from a goroutine other than the
// test's own (unlike t.Fatalf, which must run on the test goroutine): on
// any error it reports via t.Errorf and returns "", letting the caller bail
// out on its own.
func readSmartSocketCommand(t *testing.T, conn net.Conn) (string, bool) {
	t.Helper()
	lengthBuffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBuffer); err != nil {
		return "", false
	}
	var length int
	if _, err := fmt.Sscanf(string(lengthBuffer), "%04x", &length); err != nil {
		t.Errorf("failed to parse the command length: %s", err)
		return "", false
	}
	commandBuffer := make([]byte, length)
	if _, err := io.ReadFull(conn, commandBuffer); err != nil {
		t.Errorf("failed to read the command: %s", err)
		return "", false
	}
	return string(commandBuffer), true
}

// fakeAdbTransportServer accepts any number of connections, expects each to
// select a transport first (always accepted), then hands the connection and
// the second (service) command off to handle.
func fakeAdbTransportServer(t *testing.T, handle func(service string, conn net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start the fake adb server: %s", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				transportCmd, ok := readSmartSocketCommand(t, conn)
				if !ok {
					return
				}
				if !strings.HasPrefix(transportCmd, "host:transport:") {
					t.Errorf("expected a host:transport: command, got %q", transportCmd)
					return
				}
				_, _ = conn.Write([]byte("OKAY"))
				service, ok := readSmartSocketCommand(t, conn)
				if !ok {
					return
				}
				handle(service, conn)
			}()
		}
	}()

	return listener.Addr().String()
}

func newTestSmartSocket(address string) *AdbSmartSocket {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	socket := NewAdbSmartSocket(logger).(*AdbSmartSocket)
	socket.Address = address
	return socket
}

func TestDeviceList(t *testing.T) {
	address := fakeAdbServer(t, func(command string, conn net.Conn) {
		if command != "host:devices" {
			t.Errorf("expected command %q, got %q", "host:devices", command)
		}
		writeSmartSocketResponse(conn, "emulator-5554\tdevice\n192.168.1.5:5555\toffline\n")
	})

	socket := newTestSmartSocket(address)
	devices, err := socket.DeviceList()
	if err != nil {
		t.Fatalf("DeviceList failed: %s", err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devices))
	}
	if devices[0].Id != "emulator-5554" || devices[0].Type != TypeDevice {
		t.Fatalf("unexpected first device: %+v", devices[0])
	}
	if devices[1].Id != "192.168.1.5:5555" || devices[1].Type != TypeDisconnected {
		t.Fatalf("unexpected second device: %+v", devices[1])
	}
}

func TestConnectSuccess(t *testing.T) {
	address := fakeAdbServer(t, func(command string, conn net.Conn) {
		if command != "host:connect:192.168.1.5:5555" {
			t.Errorf("unexpected command: %q", command)
		}
		writeSmartSocketResponse(conn, "connected to 192.168.1.5:5555")
	})

	socket := newTestSmartSocket(address)
	if err := socket.Connect("192.168.1.5:5555"); err != nil {
		t.Fatalf("Connect failed: %s", err)
	}
}

func TestConnectFailurePropagatesServerError(t *testing.T) {
	address := fakeAdbServer(t, func(command string, conn net.Conn) {
		_, _ = conn.Write([]byte("FAIL"))
		body := "no devices/emulators found"
		_, _ = conn.Write([]byte(fmt.Sprintf("%04x", len(body))))
		_, _ = conn.Write([]byte(body))
	})

	socket := newTestSmartSocket(address)
	err := socket.Connect("192.168.1.5:5555")
	if err == nil {
		t.Fatalf("expected an error from Connect")
	}
	if err.Error() != "no devices/emulators found" {
		t.Fatalf("expected the server error message to propagate, got: %s", err)
	}
}

func TestDisconnectSuccess(t *testing.T) {
	address := fakeAdbServer(t, func(command string, conn net.Conn) {
		if command != "host:disconnect:127.0.0.1:5038" {
			t.Errorf("unexpected command: %q", command)
		}
		writeSmartSocketResponse(conn, "disconnected 127.0.0.1:5038")
	})

	socket := newTestSmartSocket(address)
	if err := socket.Disconnect("127.0.0.1:5038"); err != nil {
		t.Fatalf("Disconnect failed: %s", err)
	}
}

func TestDisconnectFailurePropagatesServerError(t *testing.T) {
	address := fakeAdbServer(t, func(command string, conn net.Conn) {
		_, _ = conn.Write([]byte("FAIL"))
		body := "no such device"
		_, _ = conn.Write([]byte(fmt.Sprintf("%04x", len(body))))
		_, _ = conn.Write([]byte(body))
	})

	socket := newTestSmartSocket(address)
	err := socket.Disconnect("127.0.0.1:5038")
	if err == nil {
		t.Fatalf("expected an error from Disconnect")
	}
	if err.Error() != "no such device" {
		t.Fatalf("expected the server error message to propagate, got: %s", err)
	}
}

func TestTransportReturnsConnectionOnSuccess(t *testing.T) {
	address := fakeAdbServer(t, func(command string, conn net.Conn) {
		if command != "host:transport:emulator-5554" {
			t.Errorf("unexpected command: %q", command)
		}
		_, _ = conn.Write([]byte("OKAY"))
		// After OKAY, the connection becomes a raw pass-through pipe.
		_, _ = conn.Write([]byte("post-transport-bytes"))
	})

	socket := newTestSmartSocket(address)
	conn, err := socket.Transport("emulator-5554")
	if err != nil {
		t.Fatalf("Transport failed: %s", err)
	}
	defer conn.Close()

	buffer := make([]byte, len("post-transport-bytes"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("failed to read the pass-through bytes: %s", err)
	}
	if string(buffer) != "post-transport-bytes" {
		t.Fatalf("expected %q, got %q", "post-transport-bytes", buffer)
	}
}

func TestTransportFailurePropagatesServerError(t *testing.T) {
	address := fakeAdbServer(t, func(command string, conn net.Conn) {
		_, _ = conn.Write([]byte("FAIL"))
		body := "device not found"
		_, _ = conn.Write([]byte(fmt.Sprintf("%04x", len(body))))
		_, _ = conn.Write([]byte(body))
	})

	socket := newTestSmartSocket(address)
	_, err := socket.Transport("missing-device")
	if err == nil {
		t.Fatalf("expected an error from Transport")
	}
}

func TestOpenStreamReturnsConnectionOnSuccess(t *testing.T) {
	address := fakeAdbTransportServer(t, func(service string, conn net.Conn) {
		if service != "shell,v2,raw:echo hi" {
			t.Errorf("unexpected service: %q", service)
		}
		_, _ = conn.Write([]byte("OKAY"))
		_, _ = conn.Write([]byte("stream-bytes"))
	})

	socket := newTestSmartSocket(address)
	conn, err := socket.OpenStream("emulator-5554", "shell,v2,raw:echo hi")
	if err != nil {
		t.Fatalf("OpenStream failed: %s", err)
	}
	defer conn.Close()

	buffer := make([]byte, len("stream-bytes"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("failed to read the stream bytes: %s", err)
	}
	if string(buffer) != "stream-bytes" {
		t.Fatalf("expected %q, got %q", "stream-bytes", buffer)
	}
}

func TestOpenStreamPropagatesServiceFailure(t *testing.T) {
	address := fakeAdbTransportServer(t, func(service string, conn net.Conn) {
		_, _ = conn.Write([]byte("FAIL"))
		body := "unknown service"
		_, _ = conn.Write([]byte(fmt.Sprintf("%04x", len(body))))
		_, _ = conn.Write([]byte(body))
	})

	socket := newTestSmartSocket(address)
	_, err := socket.OpenStream("emulator-5554", "bogus:")
	if err == nil {
		t.Fatalf("expected an error from OpenStream")
	}
	if err.Error() != "unknown service" {
		t.Fatalf("expected the server error message to propagate, got: %s", err)
	}
}

// TestOpenStreamConcurrentCallsDoNotCorruptEachOther exercises many
// concurrent OpenStream calls against distinct services: AdbSmartSocket
// used to keep its handshake scratch buffers as shared struct fields, which
// would corrupt concurrent handshakes since the owner-side relay opens one
// stream per guest OPEN, potentially many at once.
func TestOpenStreamConcurrentCallsDoNotCorruptEachOther(t *testing.T) {
	const streamCount = 20
	address := fakeAdbTransportServer(t, func(service string, conn net.Conn) {
		_, _ = conn.Write([]byte("OKAY"))
		_, _ = conn.Write([]byte(service)) // echo the service name back as the "stream data"
	})

	socket := newTestSmartSocket(address)
	var wg sync.WaitGroup
	for i := 0; i < streamCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			service := fmt.Sprintf("shell,v2,raw:stream-%d", i)
			conn, err := socket.OpenStream("emulator-5554", service)
			if err != nil {
				t.Errorf("OpenStream(%d) failed: %s", i, err)
				return
			}
			defer conn.Close()

			buffer := make([]byte, len(service))
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := io.ReadFull(conn, buffer); err != nil {
				t.Errorf("stream %d: failed to read echoed service: %s", i, err)
				return
			}
			if string(buffer) != service {
				t.Errorf("stream %d: expected echoed service %q, got %q", i, service, buffer)
			}
		}(i)
	}
	wg.Wait()
}
