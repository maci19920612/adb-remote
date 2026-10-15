package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/e2e"
	"adb-remote.maci.team/client/identity"
	"adb-remote.maci.team/client/relay"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	guestIdentity, err := identity.Load(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatalf("failed to create a test identity: %s", err)
	}
	return guestIdentity
}

// respondToJoinRoom plays the room owner on the fake transporter's end of
// the guest's connection: it reads the join request for ROOM1 and accepts
// it with a genuine key exchange (or declines it), returning the owner's
// session with the guest when accepting.
func respondToJoinRoom(t *testing.T, server net.Conn, accepted int) *e2e.Session {
	t.Helper()
	ownerIdentity := testIdentity(t)
	request := readMessage(t, server)
	if request.Command() != protocol.CommandJoinRoom {
		t.Fatalf("expected a join room request, got %x", request.Command())
	}
	payload, err := request.GetPayloadConnectRoom()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}

	result := &protocol.TransporterMessagePayloadConnectRoomResult{Accepted: accepted, ClientId: "OWNER1", PublicKey: ownerIdentity.PublicKey}
	var session *e2e.Session
	if accepted != 0 {
		var answer e2e.KeyExchange
		answer, session, err = e2e.Accept(ownerIdentity, "ROOM1", payload.PublicKey, e2e.KeyExchange{EphemeralKey: payload.EphemeralKey, Signature: payload.Signature})
		if err != nil {
			t.Fatalf("the guest's join request failed verification: %s", err)
		}
		result.EphemeralKey = answer.EphemeralKey
		result.Signature = answer.Signature
	}
	writeJoinResult(t, server, result)
	return session
}

func writeJoinResult(t *testing.T, server net.Conn, result *protocol.TransporterMessagePayloadConnectRoomResult) {
	t.Helper()
	response := protocol.CreateTransporterMessage()
	response.SetResponseCommand(protocol.CommandJoinRoom)
	if err := response.SetPayloadConnectRoomResult(result); err != nil {
		t.Fatalf("SetPayloadConnectRoomResult failed: %s", err)
	}
	if err := response.Write(server); err != nil {
		t.Fatalf("failed to write the response: %s", err)
	}
}

func TestRoomJoinStepAccepted(t *testing.T) {
	client, server := newConnectedClient(t)
	guestIdentity := testIdentity(t)

	done := make(chan error, 1)
	go func() { done <- roomJoinStep(client, guestIdentity, "ROOM1", nil) }()

	respondToJoinRoom(t, server, 1)

	if err := <-done; err != nil {
		t.Fatalf("roomJoinStep failed: %s", err)
	}
}

func TestRoomJoinStepDenied(t *testing.T) {
	client, server := newConnectedClient(t)
	guestIdentity := testIdentity(t)

	done := make(chan error, 1)
	go func() { done <- roomJoinStep(client, guestIdentity, "ROOM1", nil) }()

	respondToJoinRoom(t, server, 0)

	err := <-done
	var denied *ErrJoinRoomDenied
	if !errors.As(err, &denied) {
		t.Fatalf("expected an ErrJoinRoomDenied, got %v", err)
	}
	if denied.RoomId != "ROOM1" {
		t.Fatalf("expected room id %q, got %q", "ROOM1", denied.RoomId)
	}
}

// TestRoomJoinStepSendsPublicKey verifies the guest's identity public key
// actually goes out on the wire with the join request, since that's what
// lets the room owner display and verify its fingerprint before accepting.
func TestRoomJoinStepSendsPublicKey(t *testing.T) {
	client, server := newConnectedClient(t)
	guestIdentity := testIdentity(t)

	done := make(chan error, 1)
	go func() { done <- roomJoinStep(client, guestIdentity, "ROOM1", nil) }()

	request := readMessage(t, server)
	if request.Command() != protocol.CommandJoinRoom {
		t.Fatalf("expected a join room request, got %x", request.Command())
	}
	payload, err := request.GetPayloadConnectRoom()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}
	if !bytes.Equal(payload.PublicKey, guestIdentity.PublicKey) {
		t.Fatalf("expected the join request to carry the guest's public key %x, got %x", []byte(guestIdentity.PublicKey), payload.PublicKey)
	}
	offer := e2e.KeyExchange{EphemeralKey: payload.EphemeralKey, Signature: payload.Signature}
	if err := e2e.VerifyOffer("ROOM1", payload.PublicKey, offer); err != nil {
		t.Fatalf("expected the join request to carry an offer signed by the guest: %s", err)
	}

	writeJoinResult(t, server, &protocol.TransporterMessagePayloadConnectRoomResult{Accepted: 0, PublicKey: testIdentity(t).PublicKey})
	<-done
}

// TestRoomJoinStepRejectsAForgedAcceptance covers a relay that forwards the
// owner's genuine public key (so the fingerprint matches) with a key
// exchange of its own: the acceptance isn't signed by that key, so the
// guest refuses it and never starts relaying.
func TestRoomJoinStepRejectsAForgedAcceptance(t *testing.T) {
	client, server := newConnectedClient(t)
	guestIdentity := testIdentity(t)

	done := make(chan error, 1)
	go func() { done <- roomJoinStep(client, guestIdentity, "ROOM1", nil) }()

	request, err := readMessage(t, server).GetPayloadConnectRoom()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}
	owner := testIdentity(t)
	relay := testIdentity(t)
	answer, _, err := e2e.Accept(relay, "ROOM1", request.PublicKey, e2e.KeyExchange{EphemeralKey: request.EphemeralKey, Signature: request.Signature})
	if err != nil {
		t.Fatalf("Accept failed: %s", err)
	}
	writeJoinResult(t, server, &protocol.TransporterMessagePayloadConnectRoomResult{
		Accepted:     1,
		ClientId:     "OWNER1",
		PublicKey:    owner.PublicKey,
		EphemeralKey: answer.EphemeralKey,
		Signature:    answer.Signature,
	})

	err = <-done
	var denied *ErrJoinRoomDenied
	if err == nil || errors.As(err, &denied) {
		t.Fatalf("expected the forged acceptance to be refused, got %v", err)
	}
	probe := adb.CreateMessage()
	if err := probe.Set(adb.CommandOpen, 1, 0, []byte("shell:")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := client.SendAdbMessage(probe); !errors.Is(err, transportLayer.ErrNoSession) {
		t.Fatalf("expected no session after a refused acceptance, got %v", err)
	}
}

// TestRoomJoinStepReportsTransporterError is a regression test: a real live
// run surfaced that a transporter-side error response (e.g. "room not
// found", sent with CommandErrorResponseMask rather than
// CommandResponseMask) was falling through to the generic "unexpected
// command" branch instead of being reported as the actual server error,
// because — unlike Handshake and createRoom — roomJoinStep never checked
// message.IsError() first.
func TestRoomJoinStepReportsTransporterError(t *testing.T) {
	client, server := newConnectedClient(t)
	guestIdentity := testIdentity(t)

	done := make(chan error, 1)
	go func() { done <- roomJoinStep(client, guestIdentity, "ROOM1", nil) }()

	request := readMessage(t, server)
	if request.Command() != protocol.CommandJoinRoom {
		t.Fatalf("expected a join room request, got %x", request.Command())
	}
	response := protocol.CreateTransporterMessage()
	response.SetErrorResponseCommand(protocol.CommandJoinRoom)
	if err := response.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    protocol.ErrorRoomNotFound,
		ErrorMessage: "room not found",
	}); err != nil {
		t.Fatalf("SetErrorPayload failed: %s", err)
	}
	if err := response.Write(server); err != nil {
		t.Fatalf("failed to write the response: %s", err)
	}

	err := <-done
	if err == nil {
		t.Fatalf("expected an error")
	}
	var denied *ErrJoinRoomDenied
	if errors.As(err, &denied) {
		t.Fatalf("expected a transporter error, not ErrJoinRoomDenied: %v", err)
	}
	if !strings.Contains(err.Error(), "room not found") {
		t.Fatalf("expected the error to surface the server's message, got: %s", err)
	}
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

// fakeGuestSmartSocket records Connect/Disconnect calls so tests can assert
// JoinAsGuest drives the automatic "adb connect"/"adb disconnect" lifecycle
// correctly.
type fakeGuestSmartSocket struct {
	adb.IAdbSmartSocket

	mu              sync.Mutex
	connectCalls    []string
	disconnectCalls []string
	connectErr      error
	disconnectErr   error
}

func (f *fakeGuestSmartSocket) Connect(targetSerial string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls = append(f.connectCalls, targetSerial)
	return f.connectErr
}

func (f *fakeGuestSmartSocket) Disconnect(targetSerial string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls = append(f.disconnectCalls, targetSerial)
	return f.disconnectErr
}

func (f *fakeGuestSmartSocket) calls() (connect []string, disconnect []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.connectCalls...), append([]string{}, f.disconnectCalls...)
}

// TestJoinAsGuestRelaysAdbTraffic exercises the full guest-side path: join a
// room, stand up the local AdbProxy, and confirm ADB traffic is relayed in
// both directions once a local "adb server" connects.
func TestJoinAsGuestRelaysAdbTraffic(t *testing.T) {
	client, server := newConnectedClient(t)
	port := freeLocalPort(t)
	smartSocket := &fakeGuestSmartSocket{}
	guestIdentity := testIdentity(t)

	var events []GuestEvent
	var eventsMu sync.Mutex
	onEvent := func(e GuestEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, e)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- JoinAsGuest(ctx, client, smartSocket, guestIdentity, "ROOM1", port, onEvent) }()

	ownerSession := respondToJoinRoom(t, server, 1)

	// Simulate the local ADB server connecting to our proxy. The proxy is
	// started asynchronously right after the join room response is
	// processed, so retry the dial until it comes up.
	var localConn net.Conn
	for i := 0; i < 100; i++ {
		conn, err := net.Dial("tcp", "127.0.0.1:"+port)
		if err == nil {
			localConn = conn
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if localConn == nil {
		t.Fatalf("failed to dial the local proxy")
	}
	defer localConn.Close()

	cnxnRequest := adb.CreateMessage()
	if err := cnxnRequest.Set(adb.CommandConnect, 1, adb.MaxPayloadLength, []byte("host::")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := cnxnRequest.Write(localConn); err != nil {
		t.Fatalf("failed to write the CNXN request: %s", err)
	}
	cnxnResponse := adb.CreateMessage()
	_ = localConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := cnxnResponse.Read(localConn); err != nil {
		t.Fatalf("failed to read the CNXN response: %s", err)
	}

	// Local -> remote: the local side writes an OPEN, the transporter must
	// see it wrapped in a CommandAdbTransport message.
	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 7, 0, []byte("shell:")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := openMessage.Write(localConn); err != nil {
		t.Fatalf("failed to write the OPEN message: %s", err)
	}

	forwarded := readMessage(t, server)
	if forwarded.Command() != protocol.CommandAdbTransport {
		t.Fatalf("expected command %x, got %x", protocol.CommandAdbTransport, forwarded.Command())
	}
	opened, err := ownerSession.Open(nil, forwarded.Payload())
	if err != nil {
		t.Fatalf("the owner failed to decrypt the forwarded message: %s", err)
	}
	decoded, err := adb.DecodeMessage(opened)
	if err != nil {
		t.Fatalf("DecodeMessage failed: %s", err)
	}
	if decoded.Command() != adb.CommandOpen || decoded.DataString() != "shell:" {
		t.Fatalf("unexpected forwarded message: %+v", decoded)
	}

	// Remote -> local: the transporter sends an OKAY, the local ADB server
	// must receive it verbatim.
	okayMessage := adb.CreateMessage()
	if err := okayMessage.Set(adb.CommandOkay, 7, 0, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	wrapper := protocol.CreateTransporterMessage()
	wrapper.SetDirectCommand(protocol.CommandAdbTransport)
	if err := wrapper.SetRawPayload(ownerSession.Seal(nil, okayMessage.Bytes())); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := wrapper.Write(server); err != nil {
		t.Fatalf("failed to write the wrapped OKAY: %s", err)
	}

	received := adb.CreateMessage()
	_ = localConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := received.Read(localConn); err != nil {
		t.Fatalf("failed to read the relayed OKAY: %s", err)
	}
	if received.Command() != adb.CommandOkay {
		t.Fatalf("expected command %x, got %x", adb.CommandOkay, received.Command())
	}

	eventsMu.Lock()
	gotKinds := make([]GuestEventKind, len(events))
	for i, e := range events {
		gotKinds[i] = e.Kind
	}
	eventsMu.Unlock()
	wantKinds := []GuestEventKind{GuestJoinDecided, GuestProxyReady, GuestAdbConnected, GuestLocalAdbConnected}
	if len(gotKinds) < len(wantKinds) {
		t.Fatalf("expected at least %v, got %v", wantKinds, gotKinds)
	}
	for i, want := range wantKinds {
		if gotKinds[i] != want {
			t.Fatalf("expected event #%d to be %v, got %v (all: %v)", i, want, gotKinds[i], gotKinds)
		}
	}

	// JoinAsGuest must have run the automatic "adb connect" against its own
	// proxy address, so the operator never has to run it by hand.
	connectCalls, _ := smartSocket.calls()
	wantAddress := "127.0.0.1:" + port
	if len(connectCalls) != 1 || connectCalls[0] != wantAddress {
		t.Fatalf("expected exactly one Connect(%q) call, got %v", wantAddress, connectCalls)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("JoinAsGuest did not stop after context cancellation")
	}

	// And symmetrically disconnect once JoinAsGuest returns, so a stale
	// entry doesn't linger in `adb devices`.
	_, disconnectCalls := smartSocket.calls()
	if len(disconnectCalls) != 1 || disconnectCalls[0] != wantAddress {
		t.Fatalf("expected exactly one Disconnect(%q) call after shutdown, got %v", wantAddress, disconnectCalls)
	}
}

// TestJoinAsGuestStopsWhenTransporterDisconnectsWhileIdle is a regression
// test for the guest having no other way to notice the room owner (or the
// transporter itself) is gone while it isn't actively relaying: before this
// fix, JoinAsGuest only watched ctx.Done() and proxy.Connections() while
// idle, so it would block forever instead of returning.
func TestJoinAsGuestStopsWhenTransporterDisconnectsWhileIdle(t *testing.T) {
	client, server := newConnectedClient(t)
	port := freeLocalPort(t)
	smartSocket := &fakeGuestSmartSocket{}
	guestIdentity := testIdentity(t)

	var events []GuestEvent
	var eventsMu sync.Mutex
	onEvent := func(e GuestEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, e)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- JoinAsGuest(ctx, client, smartSocket, guestIdentity, "ROOM1", port, onEvent) }()

	respondToJoinRoom(t, server, 1)

	// Idle: no local adb server has connected yet. Disconnecting here must
	// still be noticed.
	_ = server.Close()

	select {
	case err := <-done:
		if !errors.Is(err, relay.ErrTransportClosed) {
			t.Fatalf("expected relay.ErrTransportClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("JoinAsGuest did not stop after the transporter disconnected while idle")
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	found := false
	for _, e := range events {
		if e.Kind == GuestTransportLost {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a GuestTransportLost event, got %+v", events)
	}
}

// TestJoinAsGuestReportsAutomaticAdbConnectFailure verifies that a failure
// running the automatic "adb connect" is reported as an event (so the
// operator can fall back to running it manually) rather than aborting
// JoinAsGuest entirely — relaying should still work once a local adb server
// connects some other way.
func TestJoinAsGuestReportsAutomaticAdbConnectFailure(t *testing.T) {
	client, server := newConnectedClient(t)
	port := freeLocalPort(t)
	connectErr := errors.New("adb-server unreachable")
	smartSocket := &fakeGuestSmartSocket{connectErr: connectErr}
	guestIdentity := testIdentity(t)

	events := make(chan GuestEvent, 10)
	onEvent := func(e GuestEvent) { events <- e }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- JoinAsGuest(ctx, client, smartSocket, guestIdentity, "ROOM1", port, onEvent) }()

	respondToJoinRoom(t, server, 1)

	for {
		select {
		case e := <-events:
			if e.Kind == GuestAdbConnectFailed {
				if !errors.Is(e.Err, connectErr) {
					t.Fatalf("expected the Connect error to be reported, got %v", e.Err)
				}
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for GuestAdbConnectFailed")
		}
	}
}
