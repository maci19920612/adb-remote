package adb

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
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
