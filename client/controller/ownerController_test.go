package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/shared/protocol"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

func respondToCreateRoom(t *testing.T, server net.Conn, roomId string) {
	t.Helper()
	request := readMessage(t, server)
	if request.Command() != protocol.CommandCreateRoom {
		t.Fatalf("expected a create room request, got %x", request.Command())
	}
	response := protocol.CreateTransporterMessage()
	response.SetResponseCommand(protocol.CommandCreateRoom)
	if err := response.SetPayloadCreateRoomResponse(&protocol.TransporterMessagePayloadCreateRoomResponse{RoomId: roomId}); err != nil {
		t.Fatalf("SetPayloadCreateRoomResponse failed: %s", err)
	}
	if err := response.Write(server); err != nil {
		t.Fatalf("failed to write the response: %s", err)
	}
}

func sendJoinRoomRequest(t *testing.T, server net.Conn, roomId string, guestClientId string) {
	t.Helper()
	sendJoinRoomRequestWithKey(t, server, roomId, guestClientId, nil)
}

func sendJoinRoomRequestWithKey(t *testing.T, server net.Conn, roomId string, guestClientId string, publicKey []byte) {
	t.Helper()
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandJoinRoom)
	if err := request.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{RoomId: roomId, ClientId: guestClientId, PublicKey: publicKey}); err != nil {
		t.Fatalf("SetPayloadConnectRoom failed: %s", err)
	}
	if err := request.Write(server); err != nil {
		t.Fatalf("failed to write the join room request: %s", err)
	}
}

func expectJoinResponse(t *testing.T, server net.Conn) int {
	t.Helper()
	response := readMessage(t, server)
	if response.Command() != protocol.CommandJoinRoom|protocol.CommandResponseMask {
		t.Fatalf("expected a join room response, got %x", response.Command())
	}
	payload, err := response.GetPayloadConnectRoomResponse()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	return payload.Accepted
}

func sendAdbTransport(t *testing.T, server net.Conn, adbMessage *adb.AdbMessage) {
	t.Helper()
	wrapper := protocol.CreateTransporterMessage()
	wrapper.SetDirectCommand(protocol.CommandAdbTransport)
	if err := wrapper.SetRawPayload(adbMessage.Bytes()); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := wrapper.Write(server); err != nil {
		t.Fatalf("failed to write the wrapped ADB message: %s", err)
	}
}

// expectAdbTransport reads the next message from server and returns it
// decoded as an ADB message; it fails the test if it isn't a
// CommandAdbTransport wrapper.
func expectAdbTransport(t *testing.T, server net.Conn) *adb.AdbMessage {
	t.Helper()
	forwarded := readMessage(t, server)
	if forwarded.Command() != protocol.CommandAdbTransport {
		t.Fatalf("expected command %x, got %x", protocol.CommandAdbTransport, forwarded.Command())
	}
	decoded, err := adb.DecodeMessage(forwarded.Payload())
	if err != nil {
		t.Fatalf("DecodeMessage failed: %s", err)
	}
	return decoded
}

func TestCreateRoomSuccess(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan struct {
		roomId string
		err    error
	}, 1)
	go func() {
		roomId, err := createRoom(client)
		done <- struct {
			roomId string
			err    error
		}{roomId, err}
	}()

	respondToCreateRoom(t, server, "ROOM42")

	result := <-done
	if result.err != nil {
		t.Fatalf("createRoom failed: %s", result.err)
	}
	if result.roomId != "ROOM42" {
		t.Fatalf("expected room id %q, got %q", "ROOM42", result.roomId)
	}
}

func TestHandleJoinRequestAccepted(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleJoinRequest(client, func(clientId string, publicKey []byte) (bool, error) {
			if clientId != "GUEST1" {
				t.Errorf("expected guest client id %q, got %q", "GUEST1", clientId)
			}
			return true, nil
		}, nil, "GUEST1", nil)
	}()

	if accepted := expectJoinResponse(t, server); accepted != 1 {
		t.Fatalf("expected Accepted=1, got %d", accepted)
	}
	<-done
}

func TestHandleJoinRequestDeclined(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleJoinRequest(client, func(clientId string, publicKey []byte) (bool, error) { return false, nil }, nil, "GUEST1", nil)
	}()

	if accepted := expectJoinResponse(t, server); accepted != 0 {
		t.Fatalf("expected Accepted=0, got %d", accepted)
	}
	<-done
}

// fakeSmartSocket hands out a preconfigured net.Conn per requested service,
// standing in for connections to the local device.
type fakeSmartSocket struct {
	adb.IAdbSmartSocket

	mu        sync.Mutex
	byService map[string]net.Conn
}

func newFakeSmartSocket() *fakeSmartSocket {
	return &fakeSmartSocket{byService: make(map[string]net.Conn)}
}

func (f *fakeSmartSocket) withStream(service string, conn net.Conn) *fakeSmartSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byService[service] = conn
	return f
}

func (f *fakeSmartSocket) OpenStream(targetSerial string, service string) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	conn, ok := f.byService[service]
	if !ok {
		return nil, fmt.Errorf("no fake stream configured for service %q", service)
	}
	return conn, nil
}

func expectOwnerEvent(t *testing.T, events <-chan OwnerEvent) OwnerEvent {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for an owner event")
		return OwnerEvent{}
	}
}

// TestJoinAsRoomOwnerRelaysAdbTraffic exercises the full owner-side path:
// create a room, accept a join request, open a stream for a service, and
// confirm ADB traffic is relayed correctly in both directions, including
// flow control (WRTE must wait for OKAY) and a clean stream close.
func TestJoinAsRoomOwnerRelaysAdbTraffic(t *testing.T) {
	client, server := newConnectedClient(t)
	deviceConn, ownerSideConn := net.Pipe()
	defer deviceConn.Close()
	smartSocket := newFakeSmartSocket().withStream("shell,v2,raw:echo hi", ownerSideConn)

	events := make(chan OwnerEvent, 10)
	onEvent := func(e OwnerEvent) { events <- e }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- JoinAsRoomOwner(ctx, client, smartSocket, "emulator-5554", func(clientId string, publicKey []byte) (bool, error) {
			return true, nil
		}, onEvent)
	}()

	// The room id and the guest's join request/decision must have been
	// reported as events, since JoinAsRoomOwner no longer prints anything
	// itself — that's now entirely the caller's (e.g. the TUI's) job.
	respondToCreateRoom(t, server, "ROOM7")
	roomCreated := expectOwnerEvent(t, events)
	if roomCreated.Kind != OwnerRoomCreated || roomCreated.RoomId != "ROOM7" {
		t.Fatalf("expected OwnerRoomCreated with room id ROOM7, got %+v", roomCreated)
	}

	guestPublicKey := []byte{0xaa, 0xbb, 0xcc}
	sendJoinRoomRequestWithKey(t, server, "ROOM7", "GUEST1", guestPublicKey)
	if accepted := expectJoinResponse(t, server); accepted != 1 {
		t.Fatalf("expected the join request to be accepted, got Accepted=%d", accepted)
	}

	joinRequested := expectOwnerEvent(t, events)
	if joinRequested.Kind != OwnerJoinRequested || joinRequested.GuestClientId != "GUEST1" {
		t.Fatalf("expected a join request event from GUEST1, got %+v", joinRequested)
	}
	if !bytes.Equal(joinRequested.GuestPublicKey, guestPublicKey) {
		t.Fatalf("expected the join request event to carry the guest's public key %x, got %x", guestPublicKey, joinRequested.GuestPublicKey)
	}
	joinDecided := expectOwnerEvent(t, events)
	if joinDecided.Kind != OwnerJoinDecided || joinDecided.GuestClientId != "GUEST1" || !joinDecided.Accepted {
		t.Fatalf("expected GUEST1 to be reported as accepted, got %+v", joinDecided)
	}

	// Guest opens a stream.
	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 5, 0, []byte("shell,v2,raw:echo hi\x00")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	sendAdbTransport(t, server, openMessage)

	// Owner must acknowledge with OKAY(ownId, guestId=5).
	okay := expectAdbTransport(t, server)
	if okay.Command() != adb.CommandOkay {
		t.Fatalf("expected OKAY, got %x", okay.Command())
	}
	if okay.Arg2() != 5 {
		t.Fatalf("expected the OKAY to ack guest id 5, got %d", okay.Arg2())
	}
	ownId := okay.Arg1()

	// Device -> guest: bytes written on the device connection must arrive
	// as a WRTE to the guest.
	if _, err := deviceConn.Write([]byte("hi\n")); err != nil {
		t.Fatalf("failed to write from the device: %s", err)
	}
	wrte := expectAdbTransport(t, server)
	if wrte.Command() != adb.CommandWrite || wrte.DataString() != "hi\n" {
		t.Fatalf("expected WRTE %q, got command=%x data=%q", "hi\n", wrte.Command(), wrte.DataString())
	}
	if wrte.Arg1() != ownId || wrte.Arg2() != 5 {
		t.Fatalf("expected WRTE(arg1=%d, arg2=5), got arg1=%d arg2=%d", ownId, wrte.Arg1(), wrte.Arg2())
	}

	// The owner must not send a second WRTE before the guest OKAYs the
	// first one (flow control). Write more data locally, then only after
	// confirming nothing arrives do we send the OKAY.
	if _, err := deviceConn.Write([]byte("more\n")); err != nil {
		t.Fatalf("failed to write from the device: %s", err)
	}
	_ = server.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	stalled := protocol.CreateTransporterMessage()
	if err := stalled.Read(server); err == nil {
		t.Fatalf("expected no WRTE before the guest OKAYs the previous one, got command %x", stalled.Command())
	}
	_ = server.SetReadDeadline(time.Time{})

	guestOkay := adb.CreateMessage()
	if err := guestOkay.Set(adb.CommandOkay, 5, ownId, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	sendAdbTransport(t, server, guestOkay)

	wrte2 := expectAdbTransport(t, server)
	if wrte2.Command() != adb.CommandWrite || wrte2.DataString() != "more\n" {
		t.Fatalf("expected the queued WRTE %q after the OKAY, got command=%x data=%q", "more\n", wrte2.Command(), wrte2.DataString())
	}

	// Guest -> device: a WRTE from the guest must be written to the device
	// connection, and acknowledged with an OKAY.
	guestWrite := adb.CreateMessage()
	if err := guestWrite.Set(adb.CommandWrite, 5, ownId, []byte("input")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	sendAdbTransport(t, server, guestWrite)

	deviceBuffer := make([]byte, len("input"))
	_ = deviceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := deviceConn.Read(deviceBuffer); err != nil {
		t.Fatalf("failed to read what the owner wrote to the device: %s", err)
	}
	if string(deviceBuffer) != "input" {
		t.Fatalf("expected the device to receive %q, got %q", "input", deviceBuffer)
	}
	ackForWrite := expectAdbTransport(t, server)
	if ackForWrite.Command() != adb.CommandOkay || ackForWrite.Arg1() != ownId || ackForWrite.Arg2() != 5 {
		t.Fatalf("expected OKAY(%d, 5) acking the guest's WRTE, got command=%x arg1=%d arg2=%d", ownId, ackForWrite.Command(), ackForWrite.Arg1(), ackForWrite.Arg2())
	}

	// The device closing its end must be relayed as a CLSE to the guest.
	_ = deviceConn.Close()
	clse := expectAdbTransport(t, server)
	if clse.Command() != adb.CommandClose {
		t.Fatalf("expected CLSE after the device closed, got %x", clse.Command())
	}
	if clse.Arg1() != ownId || clse.Arg2() != 5 {
		t.Fatalf("expected CLSE(%d, 5), got arg1=%d arg2=%d", ownId, clse.Arg1(), clse.Arg2())
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("JoinAsRoomOwner did not stop after context cancellation")
	}
}

// TestJoinRequestDoesNotBlockActiveStreamTraffic is a regression test for
// the bug this multiplexer fixes: prompting for a second guest's join
// request (which can block indefinitely on TTY input) must not stall ADB
// traffic relay for a stream that is already open.
func TestJoinRequestDoesNotBlockActiveStreamTraffic(t *testing.T) {
	client, server := newConnectedClient(t)
	deviceConn, ownerSideConn := net.Pipe()
	defer deviceConn.Close()
	smartSocket := newFakeSmartSocket().withStream("shell,v2,raw:echo hi", ownerSideConn)

	guest2Prompted := make(chan struct{})
	unblockGuest2 := make(chan struct{})
	promptAccept := func(clientId string, publicKey []byte) (bool, error) {
		if clientId == "GUEST2" {
			close(guest2Prompted)
			<-unblockGuest2
		}
		return true, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- JoinAsRoomOwner(ctx, client, smartSocket, "emulator-5554", promptAccept, nil)
	}()

	respondToCreateRoom(t, server, "ROOM7")

	// GUEST1 joins and is accepted immediately (its prompt does not block).
	sendJoinRoomRequest(t, server, "ROOM7", "GUEST1")
	if accepted := expectJoinResponse(t, server); accepted != 1 {
		t.Fatalf("expected GUEST1 to be accepted, got Accepted=%d", accepted)
	}

	// GUEST1 opens a stream.
	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 5, 0, []byte("shell,v2,raw:echo hi\x00")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	sendAdbTransport(t, server, openMessage)
	okay := expectAdbTransport(t, server)
	ownId := okay.Arg1()

	// GUEST2 tries to join; its prompt blocks indefinitely until released.
	sendJoinRoomRequest(t, server, "ROOM7", "GUEST2")
	select {
	case <-guest2Prompted:
	case <-time.After(2 * time.Second):
		t.Fatalf("GUEST2's join request was never prompted")
	}

	// While GUEST2's prompt is still blocked, GUEST1's already-open stream
	// must keep relaying traffic.
	if _, err := deviceConn.Write([]byte("still-flowing\n")); err != nil {
		t.Fatalf("failed to write from the device: %s", err)
	}
	wrte := expectAdbTransport(t, server)
	if wrte.Command() != adb.CommandWrite || wrte.DataString() != "still-flowing\n" {
		t.Fatalf("expected traffic to keep flowing while GUEST2's prompt is blocked, got command=%x data=%q", wrte.Command(), wrte.DataString())
	}
	if wrte.Arg1() != ownId {
		t.Fatalf("expected the WRTE for GUEST1's existing stream, got arg1=%d", wrte.Arg1())
	}

	// Releasing GUEST2's prompt must let its join complete.
	close(unblockGuest2)
	if accepted := expectJoinResponse(t, server); accepted != 1 {
		t.Fatalf("expected GUEST2 to eventually be accepted, got Accepted=%d", accepted)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("JoinAsRoomOwner did not stop after context cancellation")
	}
}
