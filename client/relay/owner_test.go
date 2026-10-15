package relay

import (
	"adb-remote.maci.team/client/adb"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOwnerSmartSocket hands out a preconfigured net.Conn per requested
// service, or a configured error, and records every requested service.
type fakeOwnerSmartSocket struct {
	adb.IAdbSmartSocket

	mu        sync.Mutex
	byService map[string]net.Conn
	// byPrefix serves services whose exact text isn't known up front (e.g.
	// a reverse forward rewritten to name an ephemeral port).
	byPrefix  map[string]func() net.Conn
	requested []string
	err       error
}

func newFakeOwnerSmartSocket() *fakeOwnerSmartSocket {
	return &fakeOwnerSmartSocket{
		byService: make(map[string]net.Conn),
		byPrefix:  make(map[string]func() net.Conn),
	}
}

func (f *fakeOwnerSmartSocket) withStream(service string, conn net.Conn) *fakeOwnerSmartSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byService[service] = conn
	return f
}

func (f *fakeOwnerSmartSocket) withStreamPrefix(prefix string, newConn func() net.Conn) *fakeOwnerSmartSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byPrefix[prefix] = newConn
	return f
}

func (f *fakeOwnerSmartSocket) requestedServices() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.requested...)
}

func (f *fakeOwnerSmartSocket) OpenStream(targetSerial string, service string) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requested = append(f.requested, service)
	if f.err != nil {
		return nil, f.err
	}
	if conn, ok := f.byService[service]; ok {
		return conn, nil
	}
	for prefix, newConn := range f.byPrefix {
		if strings.HasPrefix(service, prefix) {
			return newConn(), nil
		}
	}
	return nil, errors.New("no fake stream configured for service " + service)
}

// TestOwnerMultiplexerOpenFailureSendsClose exercises the case where the
// local device rejects the requested service: the guest must receive a
// CLSE instead of the multiplexer silently dropping the request.
func TestOwnerMultiplexerOpenFailureSendsClose(t *testing.T) {
	client := newFakeTransportClient()
	smartSocket := newFakeOwnerSmartSocket()
	smartSocket.err = errors.New("device offline")

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())

	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 5, 0, []byte("shell:whoami\x00")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, openMessage)
	m.Dispatch(<-client.messages)

	select {
	case raw := <-client.sent:
		decoded, err := adb.DecodeMessage(raw)
		if err != nil {
			t.Fatalf("DecodeMessage failed: %s", err)
		}
		if decoded.Command() != adb.CommandClose {
			t.Fatalf("expected CLSE, got %x", decoded.Command())
		}
		if decoded.Arg2() != 5 {
			t.Fatalf("expected the CLSE to reference guest id 5, got %d", decoded.Arg2())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the CLSE")
	}
}

// TestOwnerMultiplexerGuestCloseTearsDownStream verifies that a CLSE from
// the guest closes the corresponding device connection.
func TestOwnerMultiplexerGuestCloseTearsDownStream(t *testing.T) {
	client := newFakeTransportClient()
	deviceConn, ownerSideConn := net.Pipe()
	defer deviceConn.Close()
	smartSocket := newFakeOwnerSmartSocket().withStream("shell:whoami", ownerSideConn)

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())

	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 5, 0, []byte("shell:whoami\x00")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, openMessage)
	m.Dispatch(<-client.messages)

	okayBytes := <-client.sent
	okay, err := adb.DecodeMessage(okayBytes)
	if err != nil {
		t.Fatalf("DecodeMessage failed: %s", err)
	}
	ownId := okay.Arg1()

	closeMessage := adb.CreateMessage()
	if err := closeMessage.Set(adb.CommandClose, 5, ownId, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, closeMessage)
	m.Dispatch(<-client.messages)

	// The device connection must now be closed: reading from the peer end
	// must observe EOF/closed-pipe rather than blocking.
	buffer := make([]byte, 1)
	_ = deviceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := deviceConn.Read(buffer); err == nil {
		t.Fatalf("expected the device connection to be closed after a guest CLSE")
	}
}

// TestOwnerMultiplexerCloseClosesAllStreams verifies Close tears down every
// still-open stream's device connection.
func TestOwnerMultiplexerCloseClosesAllStreams(t *testing.T) {
	client := newFakeTransportClient()
	deviceConn, ownerSideConn := net.Pipe()
	defer deviceConn.Close()
	smartSocket := newFakeOwnerSmartSocket().withStream("shell:whoami", ownerSideConn)

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())

	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 5, 0, []byte("shell:whoami\x00")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, openMessage)
	m.Dispatch(<-client.messages)
	<-client.sent // the OKAY

	m.Close()

	buffer := make([]byte, 1)
	_ = deviceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := deviceConn.Read(buffer); err == nil {
		t.Fatalf("expected the device connection to be closed after Close")
	}
}

// openAndExpectClose sends the guest's OPEN for service to m and fails
// unless m answers with a CLSE for it.
func openAndExpectClose(t *testing.T, client *fakeTransportClient, m *OwnerMultiplexer, service string) {
	t.Helper()
	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 5, 0, append([]byte(service), 0)); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, openMessage)
	m.Dispatch(<-client.messages)

	select {
	case raw := <-client.sent:
		decoded, err := adb.DecodeMessage(raw)
		if err != nil {
			t.Fatalf("DecodeMessage failed: %s", err)
		}
		if decoded.Command() != adb.CommandClose || decoded.Arg2() != 5 {
			t.Fatalf("expected a CLSE for guest id 5, got command %x arg2 %d", decoded.Command(), decoded.Arg2())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the CLSE")
	}
}

// TestOwnerMultiplexerRefusesNonDeviceServices is a regression test for a
// guest driving the owner's whole adb-server: after the transport is
// selected, adb-server still runs host requests sent on the same socket
// (killing itself, connecting anywhere, switching to another device), and
// it aborts on a reverse request it doesn't know. None of those may reach
// it.
func TestOwnerMultiplexerRefusesNonDeviceServices(t *testing.T) {
	for _, service := range []string{
		"host:kill",
		"host:connect:10.0.0.1:5555",
		"host:transport:another-device",
		"host-serial:another-device:shell:id",
		"host-transport-id:3:shell:id",
		"host-usb:shell:id",
		"host-local:shell:id",
		"tport:serial:another-device",
		"reverse:bogus",
		"reverse:forward:tcp:1234",
		"reverse:forward:;tcp:1234",
		"reverse:killforward:",
		"",
		"unknown-service:arg",
	} {
		t.Run(service, func(t *testing.T) {
			client := newFakeTransportClient()
			smartSocket := newFakeOwnerSmartSocket()
			m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())

			openAndExpectClose(t, client, m, service)
			if requested := smartSocket.requestedServices(); len(requested) != 0 {
				t.Fatalf("expected %q never to reach adb-server, got %q", service, requested)
			}
		})
	}
}

// TestOwnerMultiplexerAllowsDeviceServices guards the other side: the
// services real adb clients open on a device still get through.
func TestOwnerMultiplexerAllowsDeviceServices(t *testing.T) {
	for _, service := range []string{
		"shell:",
		"shell,v2,raw:echo hi",
		"shell,v2,TERM=xterm-256color,pty:",
		"exec:cmd package list packages",
		"sync:",
		"abb_exec:package\x00install-create",
		"jdwp:1234",
		"track-jdwp",
		"track-app",
		"framebuffer:",
		"reboot:bootloader",
		"localabstract:scrcpy_1234",
		"tcp:8080",
		"reverse:list-forward",
		"reverse:killforward:localabstract:scrcpy",
		"reverse:killforward-all",
	} {
		t.Run(service, func(t *testing.T) {
			client := newFakeTransportClient()
			smartSocket := newFakeOwnerSmartSocket()
			smartSocket.err = errors.New("device offline")
			m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())

			openAndExpectClose(t, client, m, service)
			if requested := smartSocket.requestedServices(); len(requested) != 1 || requested[0] != service {
				t.Fatalf("expected %q to be requested from the device, got %q", service, requested)
			}
		})
	}
}
