package relay

import (
	"adb-remote.maci.team/client/adb"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeOwnerSmartSocket hands out a preconfigured net.Conn per requested
// service, or a configured error.
type fakeOwnerSmartSocket struct {
	adb.IAdbSmartSocket

	mu        sync.Mutex
	byService map[string]net.Conn
	err       error
}

func newFakeOwnerSmartSocket() *fakeOwnerSmartSocket {
	return &fakeOwnerSmartSocket{byService: make(map[string]net.Conn)}
}

func (f *fakeOwnerSmartSocket) withStream(service string, conn net.Conn) *fakeOwnerSmartSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byService[service] = conn
	return f
}

func (f *fakeOwnerSmartSocket) OpenStream(targetSerial string, service string) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	conn, ok := f.byService[service]
	if !ok {
		return nil, errors.New("no fake stream configured for service " + service)
	}
	return conn, nil
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
