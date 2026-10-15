package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/e2e"
	"adb-remote.maci.team/client/identity"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
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

// fakeGuest plays a guest on the fake transporter's end of the owner's
// connection: it asks to join with a genuine key exchange, and encrypts
// and decrypts the ADB traffic the way a real guest does.
type fakeGuest struct {
	t         *testing.T
	server    net.Conn
	clientId  string
	identity  *identity.Identity
	handshake *e2e.GuestHandshake
	session   *e2e.Session
}

func newFakeGuest(t *testing.T, server net.Conn, clientId string) *fakeGuest {
	return &fakeGuest{t: t, server: server, clientId: clientId, identity: testIdentity(t)}
}

// requestToJoin sends the join request the way the transporter forwards
// it: tagged with the guest's client id.
func (g *fakeGuest) requestToJoin(roomId string) {
	g.t.Helper()
	handshake, err := e2e.StartGuest(g.identity, roomId)
	if err != nil {
		g.t.Fatalf("StartGuest failed: %s", err)
	}
	g.handshake = handshake
	g.sendJoinRequest(roomId, handshake.Offer())
}

func (g *fakeGuest) sendJoinRequest(roomId string, offer e2e.KeyExchange) {
	g.t.Helper()
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandJoinRoom)
	if err := request.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{
		RoomId:       roomId,
		ClientId:     g.clientId,
		PublicKey:    g.identity.PublicKey,
		EphemeralKey: offer.EphemeralKey,
		Signature:    offer.Signature,
	}); err != nil {
		g.t.Fatalf("SetPayloadConnectRoom failed: %s", err)
	}
	if err := request.Write(g.server); err != nil {
		g.t.Fatalf("failed to write the join room request: %s", err)
	}
}

// expectDecision reads the owner's decision, which must be about this
// guest, and on acceptance completes the key exchange with ownerIdentity.
func (g *fakeGuest) expectDecision(ownerIdentity *identity.Identity) int {
	g.t.Helper()
	response := readMessage(g.t, g.server)
	if response.Command() != protocol.CommandJoinRoom|protocol.CommandResponseMask {
		g.t.Fatalf("expected a join room response, got %x", response.Command())
	}
	payload, err := response.GetPayloadConnectRoomResponse()
	if err != nil {
		g.t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	if payload.ClientId != g.clientId {
		g.t.Fatalf("expected the decision to name guest %q, got %q", g.clientId, payload.ClientId)
	}
	if !bytes.Equal(payload.PublicKey, ownerIdentity.PublicKey) {
		g.t.Fatalf("expected the decision to carry the owner's public key %x, got %x", []byte(ownerIdentity.PublicKey), payload.PublicKey)
	}
	if payload.Accepted != 0 {
		session, err := g.handshake.Finish(ownerIdentity.PublicKey, e2e.KeyExchange{EphemeralKey: payload.EphemeralKey, Signature: payload.Signature})
		if err != nil {
			g.t.Fatalf("the owner's acceptance failed verification: %s", err)
		}
		g.session = session
	}
	return payload.Accepted
}

func (g *fakeGuest) send(adbMessage *adb.AdbMessage) {
	g.t.Helper()
	wrapper := protocol.CreateTransporterMessage()
	wrapper.SetDirectCommand(protocol.CommandAdbTransport)
	if err := wrapper.SetRawPayload(g.session.Seal(nil, adbMessage.Bytes())); err != nil {
		g.t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := wrapper.Write(g.server); err != nil {
		g.t.Fatalf("failed to write the wrapped ADB message: %s", err)
	}
}

func (g *fakeGuest) sendAdb(command uint32, arg1 uint32, arg2 uint32, data []byte) {
	g.t.Helper()
	message := adb.CreateMessage()
	if err := message.Set(command, arg1, arg2, data); err != nil {
		g.t.Fatalf("Set failed: %s", err)
	}
	g.send(message)
}

// expect reads the next message from the owner, which must be an
// encrypted ADB message, and returns it decrypted.
func (g *fakeGuest) expect() *adb.AdbMessage {
	g.t.Helper()
	forwarded := readMessage(g.t, g.server)
	if forwarded.Command() != protocol.CommandAdbTransport {
		g.t.Fatalf("expected command %x, got %x", protocol.CommandAdbTransport, forwarded.Command())
	}
	opened, err := g.session.Open(nil, forwarded.Payload())
	if err != nil {
		g.t.Fatalf("failed to decrypt the owner's message: %s", err)
	}
	decoded, err := adb.DecodeMessage(opened)
	if err != nil {
		g.t.Fatalf("DecodeMessage failed: %s", err)
	}
	return decoded
}

// joinAndExpectAccepted has guest join roomId and checks it was accepted.
func (g *fakeGuest) joinAndExpectAccepted(roomId string, ownerIdentity *identity.Identity) {
	g.t.Helper()
	g.requestToJoin(roomId)
	if accepted := g.expectDecision(ownerIdentity); accepted != 1 {
		g.t.Fatalf("expected %s to be accepted, got Accepted=%d", g.clientId, accepted)
	}
}

func expectNothingFrom(t *testing.T, server net.Conn) {
	t.Helper()
	_ = server.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	unexpected := protocol.CreateTransporterMessage()
	if err := unexpected.Read(server); err == nil {
		t.Fatalf("expected nothing from the owner, got command %x", unexpected.Command())
	}
	_ = server.SetReadDeadline(time.Time{})
}

func startOwner(t *testing.T, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, ownerIdentity *identity.Identity, promptAccept AcceptPromptFunc, onEvent OwnerEventFunc) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- JoinAsRoomOwner(ctx, client, smartSocket, "emulator-5554", ownerIdentity, promptAccept, onEvent)
	}()
	t.Cleanup(cancel)
	return cancel, result
}

func acceptEveryone(string, []byte) (bool, error) {
	return true, nil
}

func expectCancelled(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("JoinAsRoomOwner did not stop after context cancellation")
	}
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
	ownerIdentity := testIdentity(t)
	guest := newFakeGuest(t, server, "GUEST1")
	handshake, err := e2e.StartGuest(guest.identity, "ROOM1")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	guest.handshake = handshake

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleJoinRequest(client, "ROOM1", ownerIdentity, func(clientId string, publicKey []byte) (bool, error) {
			if clientId != "GUEST1" {
				t.Errorf("expected guest client id %q, got %q", "GUEST1", clientId)
			}
			return true, nil
		}, nil, "GUEST1", guest.identity.PublicKey, handshake.Offer())
	}()

	if accepted := guest.expectDecision(ownerIdentity); accepted != 1 {
		t.Fatalf("expected Accepted=1, got %d", accepted)
	}
	<-done
}

func TestHandleJoinRequestDeclined(t *testing.T) {
	client, server := newConnectedClient(t)
	ownerIdentity := testIdentity(t)
	guest := newFakeGuest(t, server, "GUEST1")
	handshake, err := e2e.StartGuest(guest.identity, "ROOM1")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	guest.handshake = handshake

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleJoinRequest(client, "ROOM1", ownerIdentity, func(clientId string, publicKey []byte) (bool, error) { return false, nil }, nil, "GUEST1", guest.identity.PublicKey, handshake.Offer())
	}()

	if accepted := guest.expectDecision(ownerIdentity); accepted != 0 {
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
	requested []string
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
	f.requested = append(f.requested, service)
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
	ownerIdentity := testIdentity(t)
	cancel, done := startOwner(t, client, smartSocket, ownerIdentity, acceptEveryone, onEvent)

	// The room id and the guest's join request/decision must have been
	// reported as events, since JoinAsRoomOwner no longer prints anything
	// itself — that's now entirely the caller's (e.g. the TUI's) job.
	respondToCreateRoom(t, server, "ROOM7")
	roomCreated := expectOwnerEvent(t, events)
	if roomCreated.Kind != OwnerRoomCreated || roomCreated.RoomId != "ROOM7" {
		t.Fatalf("expected OwnerRoomCreated with room id ROOM7, got %+v", roomCreated)
	}

	guest := newFakeGuest(t, server, "GUEST1")
	guest.joinAndExpectAccepted("ROOM7", ownerIdentity)

	joinRequested := expectOwnerEvent(t, events)
	if joinRequested.Kind != OwnerJoinRequested || joinRequested.GuestClientId != "GUEST1" {
		t.Fatalf("expected a join request event from GUEST1, got %+v", joinRequested)
	}
	if !bytes.Equal(joinRequested.GuestPublicKey, guest.identity.PublicKey) {
		t.Fatalf("expected the join request event to carry the guest's public key %x, got %x", []byte(guest.identity.PublicKey), joinRequested.GuestPublicKey)
	}
	joinDecided := expectOwnerEvent(t, events)
	if joinDecided.Kind != OwnerJoinDecided || joinDecided.GuestClientId != "GUEST1" || !joinDecided.Accepted {
		t.Fatalf("expected GUEST1 to be reported as accepted, got %+v", joinDecided)
	}
	if !bytes.Equal(joinDecided.GuestPublicKey, guest.identity.PublicKey) {
		t.Fatalf("expected the join decided event to carry the guest's public key %x, got %x", []byte(guest.identity.PublicKey), joinDecided.GuestPublicKey)
	}

	// Guest opens a stream.
	guest.sendAdb(adb.CommandOpen, 5, 0, []byte("shell,v2,raw:echo hi\x00"))

	// Owner must acknowledge with OKAY(ownId, guestId=5).
	okay := guest.expect()
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
	wrte := guest.expect()
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
	expectNothingFrom(t, server)

	guest.sendAdb(adb.CommandOkay, 5, ownId, nil)

	wrte2 := guest.expect()
	if wrte2.Command() != adb.CommandWrite || wrte2.DataString() != "more\n" {
		t.Fatalf("expected the queued WRTE %q after the OKAY, got command=%x data=%q", "more\n", wrte2.Command(), wrte2.DataString())
	}

	// Guest -> device: a WRTE from the guest must be written to the device
	// connection, and acknowledged with an OKAY.
	guest.sendAdb(adb.CommandWrite, 5, ownId, []byte("input"))

	deviceBuffer := make([]byte, len("input"))
	_ = deviceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := deviceConn.Read(deviceBuffer); err != nil {
		t.Fatalf("failed to read what the owner wrote to the device: %s", err)
	}
	if string(deviceBuffer) != "input" {
		t.Fatalf("expected the device to receive %q, got %q", "input", deviceBuffer)
	}
	ackForWrite := guest.expect()
	if ackForWrite.Command() != adb.CommandOkay || ackForWrite.Arg1() != ownId || ackForWrite.Arg2() != 5 {
		t.Fatalf("expected OKAY(%d, 5) acking the guest's WRTE, got command=%x arg1=%d arg2=%d", ownId, ackForWrite.Command(), ackForWrite.Arg1(), ackForWrite.Arg2())
	}

	// The device closing its end must be relayed as a CLSE to the guest.
	_ = deviceConn.Close()
	clse := guest.expect()
	if clse.Command() != adb.CommandClose {
		t.Fatalf("expected CLSE after the device closed, got %x", clse.Command())
	}
	if clse.Arg1() != ownId || clse.Arg2() != 5 {
		t.Fatalf("expected CLSE(%d, 5), got arg1=%d arg2=%d", ownId, clse.Arg1(), clse.Arg2())
	}

	cancel()
	expectCancelled(t, done)
}

// TestJoinAsRoomOwnerHandlesGuestLeft is a regression test for the owner
// having no other way to learn its guest disconnected mid-room: a
// CommandGuestLeft notification must surface as an OwnerGuestLeft event and
// close any streams still open for that guest (since only one guest is
// ever active at a time, every open stream necessarily belonged to it).
func TestJoinAsRoomOwnerHandlesGuestLeft(t *testing.T) {
	client, server := newConnectedClient(t)
	deviceConn, ownerSideConn := net.Pipe()
	defer deviceConn.Close()
	smartSocket := newFakeSmartSocket().withStream("shell,v2,raw:echo hi", ownerSideConn)

	events := make(chan OwnerEvent, 10)
	onEvent := func(e OwnerEvent) { events <- e }
	ownerIdentity := testIdentity(t)
	cancel, done := startOwner(t, client, smartSocket, ownerIdentity, acceptEveryone, onEvent)

	respondToCreateRoom(t, server, "ROOM7")
	expectOwnerEvent(t, events) // OwnerRoomCreated

	guest := newFakeGuest(t, server, "GUEST1")
	guest.joinAndExpectAccepted("ROOM7", ownerIdentity)
	expectOwnerEvent(t, events) // OwnerJoinRequested
	expectOwnerEvent(t, events) // OwnerJoinDecided

	guest.sendAdb(adb.CommandOpen, 5, 0, []byte("shell,v2,raw:echo hi\x00"))
	guest.expect() // OKAY acknowledging the open

	guestLeft := protocol.CreateTransporterMessage()
	guestLeft.SetDirectCommand(protocol.CommandGuestLeft)
	if err := guestLeft.Write(server); err != nil {
		t.Fatalf("failed to write the guest-left notification: %s", err)
	}

	event := expectOwnerEvent(t, events)
	if event.Kind != OwnerGuestLeft {
		t.Fatalf("expected OwnerGuestLeft, got %+v", event)
	}

	// The stream opened for the now-departed guest must have been closed.
	_ = deviceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 1)
	if _, err := deviceConn.Read(buffer); err != io.EOF {
		t.Fatalf("expected the device stream to be closed after the guest left, got err=%v", err)
	}

	// The owner's own connection must be unaffected: a new guest can join,
	// with a session of its own.
	newFakeGuest(t, server, "GUEST2").joinAndExpectAccepted("ROOM7", ownerIdentity)

	cancel()
	expectCancelled(t, done)
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

	ownerIdentity := testIdentity(t)
	cancel, done := startOwner(t, client, smartSocket, ownerIdentity, promptAccept, nil)

	respondToCreateRoom(t, server, "ROOM7")

	// GUEST1 joins and is accepted immediately (its prompt does not block).
	guest1 := newFakeGuest(t, server, "GUEST1")
	guest1.joinAndExpectAccepted("ROOM7", ownerIdentity)

	// GUEST1 opens a stream.
	guest1.sendAdb(adb.CommandOpen, 5, 0, []byte("shell,v2,raw:echo hi\x00"))
	ownId := guest1.expect().Arg1()

	// GUEST2 tries to join; its prompt blocks indefinitely until released.
	guest2 := newFakeGuest(t, server, "GUEST2")
	guest2.requestToJoin("ROOM7")
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
	wrte := guest1.expect()
	if wrte.Command() != adb.CommandWrite || wrte.DataString() != "still-flowing\n" {
		t.Fatalf("expected traffic to keep flowing while GUEST2's prompt is blocked, got command=%x data=%q", wrte.Command(), wrte.DataString())
	}
	if wrte.Arg1() != ownId {
		t.Fatalf("expected the WRTE for GUEST1's existing stream, got arg1=%d", wrte.Arg1())
	}

	// Releasing GUEST2's prompt must let its join complete.
	close(unblockGuest2)
	if accepted := guest2.expectDecision(ownerIdentity); accepted != 1 {
		t.Fatalf("expected GUEST2 to eventually be accepted, got Accepted=%d", accepted)
	}

	cancel()
	expectCancelled(t, done)
}

// TestForgedJoinRequestIsDeclinedWithoutAsking covers a relay presenting a
// genuine guest's public key with a key exchange of its own: the request
// isn't signed by that key, so it is declined before the operator is shown
// a fingerprint that would look right.
func TestForgedJoinRequestIsDeclinedWithoutAsking(t *testing.T) {
	client, server := newConnectedClient(t)
	events := make(chan OwnerEvent, 10)
	ownerIdentity := testIdentity(t)
	prompted := make(chan struct{}, 1)
	promptAccept := func(string, []byte) (bool, error) {
		prompted <- struct{}{}
		return true, nil
	}
	cancel, done := startOwner(t, client, newFakeSmartSocket(), ownerIdentity, promptAccept, func(e OwnerEvent) { events <- e })

	respondToCreateRoom(t, server, "ROOM7")
	expectOwnerEvent(t, events) // OwnerRoomCreated

	guest := newFakeGuest(t, server, "GUEST1")
	impostor, err := e2e.StartGuest(testIdentity(t), "ROOM7")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	guest.sendJoinRequest("ROOM7", impostor.Offer())

	if accepted := guest.expectDecision(ownerIdentity); accepted != 0 {
		t.Fatalf("expected the forged request to be declined, got Accepted=%d", accepted)
	}
	if event := expectOwnerEvent(t, events); event.Kind != OwnerJoinFailed {
		t.Fatalf("expected OwnerJoinFailed, got %+v", event)
	}
	select {
	case <-prompted:
		t.Fatalf("expected the operator not to be asked about a forged request")
	default:
	}

	cancel()
	expectCancelled(t, done)
}

// TestAdbTrafficBeforeAcceptanceIsDropped is a regression test for a guest
// reaching the device while the operator is still deciding.
func TestAdbTrafficBeforeAcceptanceIsDropped(t *testing.T) {
	client, server := newConnectedClient(t)
	smartSocket := newFakeSmartSocket()
	ownerIdentity := testIdentity(t)
	decide := make(chan bool)
	promptAccept := func(string, []byte) (bool, error) { return <-decide, nil }
	cancel, done := startOwner(t, client, smartSocket, ownerIdentity, promptAccept, nil)

	respondToCreateRoom(t, server, "ROOM7")
	guest := newFakeGuest(t, server, "GUEST1")
	guest.requestToJoin("ROOM7")

	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 5, 0, []byte("shell:id\x00")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	early := protocol.CreateTransporterMessage()
	early.SetDirectCommand(protocol.CommandAdbTransport)
	if err := early.SetRawPayload(openMessage.Bytes()); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := early.Write(server); err != nil {
		t.Fatalf("failed to write the early ADB message: %s", err)
	}
	expectNothingFrom(t, server)
	smartSocket.mu.Lock()
	requested := len(smartSocket.requested)
	smartSocket.mu.Unlock()
	if requested != 0 {
		t.Fatalf("expected no stream to be opened before the guest was accepted")
	}

	decide <- false
	if accepted := guest.expectDecision(ownerIdentity); accepted != 0 {
		t.Fatalf("expected the guest to be declined, got Accepted=%d", accepted)
	}

	cancel()
	expectCancelled(t, done)
}

// TestRoomClosedByTheTransporterEndsTheOwnerFlow covers the transporter
// expiring a room nobody joined: the owner learns why instead of just
// losing the connection.
func TestRoomClosedByTheTransporterEndsTheOwnerFlow(t *testing.T) {
	client, server := newConnectedClient(t)
	_, done := startOwner(t, client, newFakeSmartSocket(), testIdentity(t), acceptEveryone, nil)
	respondToCreateRoom(t, server, "ROOM7")

	expired := protocol.CreateTransporterMessage()
	expired.SetErrorResponseCommand(protocol.CommandCreateRoom)
	if err := expired.SetErrorPayload(&protocol.TransporterMessagePayloadError{ErrorCode: protocol.ErrorRoomExpired, ErrorMessage: "no guest for 24h"}); err != nil {
		t.Fatalf("SetErrorPayload failed: %s", err)
	}
	if err := expired.Write(server); err != nil {
		t.Fatalf("failed to write the expiry notice: %s", err)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no guest for 24h") {
			t.Fatalf("expected the expiry reason, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("JoinAsRoomOwner did not stop after the transporter closed the room")
	}
}
