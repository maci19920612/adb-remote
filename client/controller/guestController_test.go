package controller

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func respondToJoinRoom(t *testing.T, server net.Conn, accepted int) {
	t.Helper()
	request := readMessage(t, server)
	if request.Command() != protocol.CommandJoinRoom {
		t.Fatalf("expected a join room request, got %x", request.Command())
	}

	response := protocol.CreateTransporterMessage()
	response.SetResponseCommand(protocol.CommandJoinRoom)
	if err := response.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{Accepted: accepted}); err != nil {
		t.Fatalf("SetPayloadConnectRoomResult failed: %s", err)
	}
	if err := response.Write(server); err != nil {
		t.Fatalf("failed to write the response: %s", err)
	}
}

func TestRoomJoinStepAccepted(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan error, 1)
	go func() { done <- roomJoinStep(client, "ROOM1") }()

	respondToJoinRoom(t, server, 1)

	if err := <-done; err != nil {
		t.Fatalf("roomJoinStep failed: %s", err)
	}
}

func TestRoomJoinStepDenied(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan error, 1)
	go func() { done <- roomJoinStep(client, "ROOM1") }()

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

// TestJoinAsGuestRelaysAdbTraffic exercises the full guest-side path: join a
// room, stand up the local AdbProxy, and confirm ADB traffic is relayed in
// both directions once a local "adb server" connects.
func TestJoinAsGuestRelaysAdbTraffic(t *testing.T) {
	client, server := newConnectedClient(t)
	port := freeLocalPort(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- JoinAsGuest(ctx, client, "ROOM1", port) }()

	respondToJoinRoom(t, server, 1)

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
	decoded, err := adb.DecodeMessage(forwarded.Payload())
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
	if err := wrapper.SetRawPayload(okayMessage.Bytes()); err != nil {
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

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("JoinAsGuest did not stop after context cancellation")
	}
}
