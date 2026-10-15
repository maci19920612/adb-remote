package relay

import (
	"adb-remote.maci.team/client/adb"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

var rewrittenScrcpyForward = regexp.MustCompile(`^reverse:forward:localabstract:scrcpy;((?:tcp|localfilesystem):.+)$`)

// dialRelay connects to a relay endpoint the way the owner's adb-server
// does when the device connects through the reverse forward.
func dialRelay(t *testing.T, spec string) (net.Conn, error) {
	t.Helper()
	if path, ok := strings.CutPrefix(spec, "localfilesystem:"); ok {
		return net.DialTimeout("unix", path, time.Second)
	}
	if port, ok := strings.CutPrefix(spec, "tcp:"); ok {
		return net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	}
	t.Fatalf("unexpected relay endpoint %q", spec)
	return nil, nil
}

// deliverToMultiplexer hands m an ADB message as if the guest had sent it.
func deliverToMultiplexer(t *testing.T, client *fakeTransportClient, m *OwnerMultiplexer, command uint32, arg1 uint32, arg2 uint32, data []byte) {
	t.Helper()
	message := adb.CreateMessage()
	if err := message.Set(command, arg1, arg2, data); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, message)
	m.Dispatch(<-client.messages)
}

// expectSentToGuest waits for the next ADB message sent to the guest and
// checks its command.
func expectSentToGuest(t *testing.T, client *fakeTransportClient, command uint32) *adb.AdbMessage {
	t.Helper()
	select {
	case raw := <-client.sent:
		decoded, err := adb.DecodeMessage(raw)
		if err != nil {
			t.Fatalf("DecodeMessage failed: %s", err)
		}
		if decoded.Command() != command {
			t.Fatalf("expected command %x sent to the guest, got %x", command, decoded.Command())
		}
		return decoded
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for command %x to be sent to the guest", command)
	}
	return nil
}

// respondingConn returns a connection whose peer writes reply, then waits
// for the connection to be closed.
func respondingConn(reply string) net.Conn {
	ownerSide, deviceSide := net.Pipe()
	go func() {
		defer deviceSide.Close()
		_, _ = deviceSide.Write([]byte(reply))
		_, _ = io.Copy(io.Discard, deviceSide)
	}()
	return ownerSide
}

// openScrcpyReverseForward has the guest request scrcpy's usual reverse
// forward and returns the local relay endpoint the device was asked to use
// instead of the guest's tcp:27183.
func openScrcpyReverseForward(t *testing.T, client *fakeTransportClient, smartSocket *fakeOwnerSmartSocket, m *OwnerMultiplexer, guestId uint32, local string) string {
	t.Helper()
	deliverToMultiplexer(t, client, m, adb.CommandOpen, guestId, 0, []byte("reverse:forward:localabstract:scrcpy;"+local+"\x00"))
	expectSentToGuest(t, client, adb.CommandOkay)

	requested := smartSocket.requestedServices()
	last := requested[len(requested)-1]
	match := rewrittenScrcpyForward.FindStringSubmatch(last)
	if match == nil || match[1] == local {
		t.Fatalf("expected the reverse forward to be rewritten to a local relay endpoint, the device was asked for %q", last)
	}
	return match[1]
}

func expectRelayPortClosed(t *testing.T, spec string) {
	t.Helper()
	conn, err := dialRelay(t, spec)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected relay endpoint %s to be closed", spec)
	}
}

// TestOwnerMultiplexerReverseForwardRelaysDeviceConnectionsToTheGuest
// covers how scrcpy and Android Studio mirroring connect: the guest asks for
// "reverse:forward:<device socket>;tcp:<guest port>", and the device then
// connects back through it. The guest's port is on the guest's machine, so
// the owner must not hand it to its own adb-server as is; the device's
// connection has to arrive at the guest as an OPEN for the guest's port.
func TestOwnerMultiplexerReverseForwardRelaysDeviceConnectionsToTheGuest(t *testing.T) {
	client := newFakeTransportClient()
	forwardConn, ownerSideConn := net.Pipe()
	defer forwardConn.Close()
	smartSocket := newFakeOwnerSmartSocket().withStreamPrefix("reverse:forward:", func() net.Conn { return ownerSideConn })

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())
	defer m.Close()

	port := openScrcpyReverseForward(t, client, smartSocket, m, 5, "tcp:27183")

	// The local adb-server connecting to the relay port is how a device-side
	// connection to the forward's remote end shows up on the owner.
	deviceSide, err := dialRelay(t, port)
	if err != nil {
		t.Fatalf("failed to connect to the relay port: %s", err)
	}
	defer deviceSide.Close()
	_ = deviceSide.SetDeadline(time.Now().Add(5 * time.Second))

	open := expectSentToGuest(t, client, adb.CommandOpen)
	if open.DataString() != "tcp:27183\x00" {
		t.Fatalf("expected the guest to be asked to open its own tcp:27183, got %q", open.DataString())
	}
	if open.Arg1() == 0 || open.Arg2() != 0 {
		t.Fatalf("expected OPEN(ownId != 0, 0), got OPEN(%d, %d)", open.Arg1(), open.Arg2())
	}
	ownId := open.Arg1()

	deliverToMultiplexer(t, client, m, adb.CommandOkay, 9, ownId, nil)

	if _, err := deviceSide.Write([]byte("from-device")); err != nil {
		t.Fatalf("device-side write failed: %s", err)
	}
	write := expectSentToGuest(t, client, adb.CommandWrite)
	if write.Arg1() != ownId || write.Arg2() != 9 || string(write.Data()) != "from-device" {
		t.Fatalf("unexpected WRTE(%d, %d, %q)", write.Arg1(), write.Arg2(), write.Data())
	}

	deliverToMultiplexer(t, client, m, adb.CommandWrite, 9, ownId, []byte("from-guest"))
	received := make([]byte, len("from-guest"))
	if _, err := io.ReadFull(deviceSide, received); err != nil {
		t.Fatalf("device-side read failed: %s", err)
	}
	if string(received) != "from-guest" {
		t.Fatalf("expected the device to receive %q, got %q", "from-guest", received)
	}
	okay := expectSentToGuest(t, client, adb.CommandOkay)
	if okay.Arg1() != ownId || okay.Arg2() != 9 {
		t.Fatalf("expected OKAY(%d, 9) acking the guest's WRTE, got OKAY(%d, %d)", ownId, okay.Arg1(), okay.Arg2())
	}

	deliverToMultiplexer(t, client, m, adb.CommandClose, 9, ownId, nil)
	if _, err := deviceSide.Read(make([]byte, 1)); err == nil {
		t.Fatalf("expected the device-side connection to be closed after the guest's CLSE")
	}
}

// TestOwnerMultiplexerReverseForwardRebindKeepsThePortAndRetargets verifies
// that forwarding the same device socket again reuses the relay port the
// device already knows, and sends later connections to the new guest port.
func TestOwnerMultiplexerReverseForwardRebindKeepsThePortAndRetargets(t *testing.T) {
	client := newFakeTransportClient()
	smartSocket := newFakeOwnerSmartSocket().withStreamPrefix("reverse:forward:", func() net.Conn {
		ownerSide, _ := net.Pipe()
		return ownerSide
	})

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())
	defer m.Close()

	firstPort := openScrcpyReverseForward(t, client, smartSocket, m, 5, "tcp:27183")
	secondPort := openScrcpyReverseForward(t, client, smartSocket, m, 6, "tcp:27184")
	if firstPort != secondPort {
		t.Fatalf("expected the rebind to reuse relay port %s, got %s", firstPort, secondPort)
	}

	deviceSide, err := dialRelay(t, secondPort)
	if err != nil {
		t.Fatalf("failed to connect to the relay port: %s", err)
	}
	defer deviceSide.Close()

	open := expectSentToGuest(t, client, adb.CommandOpen)
	if open.DataString() != "tcp:27184\x00" {
		t.Fatalf("expected the rebound forward to open tcp:27184 on the guest, got %q", open.DataString())
	}
}

// TestOwnerMultiplexerReverseKillForwardClosesTheRelayPort verifies that
// removing a forward is passed to the device unchanged and stops the relay
// port from accepting connections.
func TestOwnerMultiplexerReverseKillForwardClosesTheRelayPort(t *testing.T) {
	client := newFakeTransportClient()
	forwardConn, ownerSideConn := net.Pipe()
	defer forwardConn.Close()
	smartSocket := newFakeOwnerSmartSocket().
		withStreamPrefix("reverse:forward:", func() net.Conn { return ownerSideConn }).
		withStreamPrefix("reverse:killforward:", func() net.Conn { return respondingConn("OKAY") })

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())
	defer m.Close()

	port := openScrcpyReverseForward(t, client, smartSocket, m, 5, "tcp:27183")

	deliverToMultiplexer(t, client, m, adb.CommandOpen, 6, 0, []byte("reverse:killforward:localabstract:scrcpy\x00"))
	expectSentToGuest(t, client, adb.CommandOkay)

	requested := smartSocket.requestedServices()
	if last := requested[len(requested)-1]; last != "reverse:killforward:localabstract:scrcpy" {
		t.Fatalf("expected the kill request to reach the device unchanged, got %q", last)
	}
	expectRelayPortClosed(t, port)
}

// TestOwnerMultiplexerCloseRemovesReverseForwardsFromTheDevice verifies
// that a guest's reverse forwards don't outlive it: Close removes them from
// the device and closes their relay ports.
func TestOwnerMultiplexerCloseRemovesReverseForwardsFromTheDevice(t *testing.T) {
	client := newFakeTransportClient()
	forwardConn, ownerSideConn := net.Pipe()
	defer forwardConn.Close()
	smartSocket := newFakeOwnerSmartSocket().
		withStreamPrefix("reverse:forward:", func() net.Conn { return ownerSideConn }).
		withStreamPrefix("reverse:killforward:", func() net.Conn { return respondingConn("OKAY") })

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())

	port := openScrcpyReverseForward(t, client, smartSocket, m, 5, "tcp:27183")

	m.Close()

	if !slices.Contains(smartSocket.requestedServices(), "reverse:killforward:localabstract:scrcpy") {
		t.Fatalf("expected Close to remove the forward from the device, requested: %q", smartSocket.requestedServices())
	}
	expectRelayPortClosed(t, port)
}

// TestOwnerMultiplexerReverseForwardFailureClosesTheRelayPort verifies that
// a forward the local device refuses doesn't leave a relay port behind.
func TestOwnerMultiplexerReverseForwardFailureClosesTheRelayPort(t *testing.T) {
	client := newFakeTransportClient()
	smartSocket := newFakeOwnerSmartSocket()
	smartSocket.err = errors.New("device offline")

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())
	defer m.Close()

	deliverToMultiplexer(t, client, m, adb.CommandOpen, 5, 0, []byte("reverse:forward:localabstract:scrcpy;tcp:27183\x00"))
	closeMessage := expectSentToGuest(t, client, adb.CommandClose)
	if closeMessage.Arg2() != 5 {
		t.Fatalf("expected the CLSE to reference guest id 5, got %d", closeMessage.Arg2())
	}

	requested := smartSocket.requestedServices()
	match := rewrittenScrcpyForward.FindStringSubmatch(requested[len(requested)-1])
	if match == nil || match[1] == "tcp:27183" {
		t.Fatalf("expected a rewritten reverse forward request, got %q", requested)
	}
	expectRelayPortClosed(t, match[1])
}

// TestOwnerMultiplexerReverseForwardRelayIsPrivate is a regression test for
// the relay endpoint being a loopback TCP port, which any local process
// could connect to and be relayed to the guest as if it were the device.
// Where adb-server can use one, it is a Unix socket only this user can
// reach, and it disappears with the forward.
func TestOwnerMultiplexerReverseForwardRelayIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("adb-server can't connect to Unix sockets on Windows")
	}
	client := newFakeTransportClient()
	forwardConn, ownerSideConn := net.Pipe()
	defer forwardConn.Close()
	smartSocket := newFakeOwnerSmartSocket().
		withStreamPrefix("reverse:forward:", func() net.Conn { return ownerSideConn }).
		withStreamPrefix("reverse:killforward:", func() net.Conn { return respondingConn("OKAY") })

	m := NewOwnerMultiplexer(smartSocket, "emulator-5554", client, newTestLogger())
	defer m.Close()

	spec := openScrcpyReverseForward(t, client, smartSocket, m, 5, "tcp:27183")
	path, ok := strings.CutPrefix(spec, "localfilesystem:")
	if !ok {
		t.Fatalf("expected a Unix socket relay endpoint, got %q", spec)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("failed to stat the relay socket's directory: %s", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("expected the relay socket's directory to be private (0700), got %o", info.Mode().Perm())
	}

	deliverToMultiplexer(t, client, m, adb.CommandOpen, 6, 0, []byte("reverse:killforward:localabstract:scrcpy\x00"))
	expectSentToGuest(t, client, adb.CommandOkay)
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("expected the relay socket's directory to be removed with the forward, got err=%v", err)
	}
}
