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
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandJoinRoom)
	if err := request.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{RoomId: roomId, ClientId: guestClientId}); err != nil {
		t.Fatalf("SetPayloadConnectRoom failed: %s", err)
	}
	if err := request.Write(server); err != nil {
		t.Fatalf("failed to write the join room request: %s", err)
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

func TestWaitForRoomJoinRequestAccepted(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan error, 1)
	go func() {
		accepted, err := waitForRoomJoinRequest(client, func(clientId string) (bool, error) {
			if clientId != "GUEST1" {
				t.Errorf("expected guest client id %q, got %q", "GUEST1", clientId)
			}
			return true, nil
		})
		if err == nil && !accepted {
			err = errors.New("expected the request to be accepted")
		}
		done <- err
	}()

	sendJoinRoomRequest(t, server, "ROOM1", "GUEST1")

	response := readMessage(t, server)
	if response.Command() != protocol.CommandJoinRoom|protocol.CommandResponseMask {
		t.Fatalf("expected a join room response, got %x", response.Command())
	}
	payload, err := response.GetPayloadConnectRoomResponse()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	if payload.Accepted != 1 {
		t.Fatalf("expected Accepted=1, got %d", payload.Accepted)
	}

	if err := <-done; err != nil {
		t.Fatalf("waitForRoomJoinRequest failed: %s", err)
	}
}

func TestWaitForRoomJoinRequestDeclined(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan bool, 1)
	go func() {
		accepted, err := waitForRoomJoinRequest(client, func(clientId string) (bool, error) {
			return false, nil
		})
		if err != nil {
			t.Errorf("waitForRoomJoinRequest failed: %s", err)
		}
		done <- accepted
	}()

	sendJoinRoomRequest(t, server, "ROOM1", "GUEST1")
	response := readMessage(t, server)
	payload, err := response.GetPayloadConnectRoomResponse()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	if payload.Accepted != 0 {
		t.Fatalf("expected Accepted=0, got %d", payload.Accepted)
	}
	if accepted := <-done; accepted {
		t.Fatalf("expected the request to be reported as declined")
	}
}

// fakeSmartSocket hands out a fixed net.Conn from Transport, standing in
// for a real local device connection.
type fakeSmartSocket struct {
	adb.IAdbSmartSocket
	conn net.Conn
	err  error
}

func (f *fakeSmartSocket) Transport(targetSerial string) (net.Conn, error) {
	return f.conn, f.err
}

// TestJoinAsRoomOwnerRelaysAdbTraffic exercises the full owner-side path:
// create a room, accept a join request, and confirm ADB traffic is relayed
// in both directions between the local "device" and the transporter.
func TestJoinAsRoomOwnerRelaysAdbTraffic(t *testing.T) {
	client, server := newConnectedClient(t)
	deviceConn, ownerSideConn := net.Pipe()
	defer deviceConn.Close()
	smartSocket := &fakeSmartSocket{conn: ownerSideConn}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- JoinAsRoomOwner(ctx, client, smartSocket, "emulator-5554", func(clientId string) (bool, error) {
			return true, nil
		})
	}()

	respondToCreateRoom(t, server, "ROOM7")
	sendJoinRoomRequest(t, server, "ROOM7", "GUEST1")
	readMessage(t, server) // the join room accept response

	// Remote -> local: the transporter sends an OPEN, the local device must
	// receive it verbatim.
	openMessage := adb.CreateMessage()
	if err := openMessage.Set(adb.CommandOpen, 3, 0, []byte("shell:")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	wrapper := protocol.CreateTransporterMessage()
	wrapper.SetDirectCommand(protocol.CommandAdbTransport)
	if err := wrapper.SetRawPayload(openMessage.Bytes()); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := wrapper.Write(server); err != nil {
		t.Fatalf("failed to write the wrapped OPEN: %s", err)
	}

	received := adb.CreateMessage()
	_ = deviceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := received.Read(deviceConn); err != nil {
		t.Fatalf("failed to read the relayed OPEN: %s", err)
	}
	if received.Command() != adb.CommandOpen {
		t.Fatalf("expected command %x, got %x", adb.CommandOpen, received.Command())
	}

	// Local -> remote: the local device replies with OKAY, the transporter
	// must see it wrapped in a CommandAdbTransport message.
	okayMessage := adb.CreateMessage()
	if err := okayMessage.Set(adb.CommandOkay, 3, 0, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := okayMessage.Write(deviceConn); err != nil {
		t.Fatalf("failed to write the OKAY message: %s", err)
	}

	forwarded := readMessage(t, server)
	if forwarded.Command() != protocol.CommandAdbTransport {
		t.Fatalf("expected command %x, got %x", protocol.CommandAdbTransport, forwarded.Command())
	}
	decoded, err := adb.DecodeMessage(forwarded.Payload())
	if err != nil {
		t.Fatalf("DecodeMessage failed: %s", err)
	}
	if decoded.Command() != adb.CommandOkay {
		t.Fatalf("expected command %x, got %x", adb.CommandOkay, decoded.Command())
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
