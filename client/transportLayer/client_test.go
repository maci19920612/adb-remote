package transportLayer

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/internal/testtls"
	"adb-remote.maci.team/shared/protocol"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeTransporter accepts a single connection and hands it to the test via
// the returned channel, standing in for the transporter server.
func fakeTransporter(t *testing.T) (address string, connections <-chan net.Conn) {
	t.Helper()
	listener := testtls.Listen(t)

	ch := make(chan net.Conn, 1)
	go func() {
		conn, err := testtls.Accept(listener)
		if err != nil {
			return
		}
		ch <- conn
	}()
	return listener.Addr().String(), ch
}

func newConnectedTestClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	address, connections := fakeTransporter(t)
	client, err := CreateClient(newTestLogger(), &config.ClientConfiguration{TransporterAddress: address})
	if err != nil {
		t.Fatalf("CreateClient failed: %s", err)
	}
	if err := client.Start(); err != nil {
		t.Fatalf("Start failed: %s", err)
	}
	t.Cleanup(client.Close)

	select {
	case serverConn := <-connections:
		t.Cleanup(func() { _ = serverConn.Close() })
		return client, serverConn
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the client to connect")
		return nil, nil
	}
}

func TestSendConnectWritesExpectedMessage(t *testing.T) {
	client, server := newConnectedTestClient(t)

	if err := client.SendConnect(); err != nil {
		t.Fatalf("SendConnect failed: %s", err)
	}

	received := protocol.CreateTransporterMessage()
	if err := received.Read(server); err != nil {
		t.Fatalf("failed to read the message on the server side: %s", err)
	}
	if received.Command() != protocol.CommandConnect {
		t.Fatalf("expected command %x, got %x", protocol.CommandConnect, received.Command())
	}
	payload, err := received.GetPayloadConnect()
	if err != nil {
		t.Fatalf("GetPayloadConnect failed: %s", err)
	}
	if payload.ProtocolVersion != protocol.ProtocolVersion {
		t.Fatalf("expected protocol version %d, got %d", protocol.ProtocolVersion, payload.ProtocolVersion)
	}
}

func TestSendJoinRoomWritesExpectedMessage(t *testing.T) {
	client, server := newConnectedTestClient(t)

	publicKey := []byte{0x01, 0x02, 0x03}
	if err := client.SendJoinRoom("ROOM7", publicKey); err != nil {
		t.Fatalf("SendJoinRoom failed: %s", err)
	}

	received := protocol.CreateTransporterMessage()
	if err := received.Read(server); err != nil {
		t.Fatalf("failed to read the message on the server side: %s", err)
	}
	if received.Command() != protocol.CommandJoinRoom {
		t.Fatalf("expected command %x, got %x", protocol.CommandJoinRoom, received.Command())
	}
	payload, err := received.GetPayloadConnectRoom()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}
	if payload.RoomId != "ROOM7" {
		t.Fatalf("expected room id %q, got %q", "ROOM7", payload.RoomId)
	}
	if !bytes.Equal(payload.PublicKey, publicKey) {
		t.Fatalf("expected public key %x, got %x", publicKey, payload.PublicKey)
	}
}

func TestSendAdbMessageEmbedsRawAdbBytes(t *testing.T) {
	client, server := newConnectedTestClient(t)

	adbMessage := adb.CreateMessage()
	if err := adbMessage.Set(adb.CommandOpen, 1, 0, []byte("shell:")); err != nil {
		t.Fatalf("adb Set failed: %s", err)
	}
	if err := client.SendAdbMessage(adbMessage); err != nil {
		t.Fatalf("SendAdbMessage failed: %s", err)
	}

	received := protocol.CreateTransporterMessage()
	if err := received.Read(server); err != nil {
		t.Fatalf("failed to read the message on the server side: %s", err)
	}
	if received.Command() != protocol.CommandAdbTransport {
		t.Fatalf("expected command %x, got %x", protocol.CommandAdbTransport, received.Command())
	}
	decoded, err := adb.DecodeMessage(received.Payload())
	if err != nil {
		t.Fatalf("DecodeMessage failed: %s", err)
	}
	if decoded.Command() != adb.CommandOpen {
		t.Fatalf("expected adb command %x, got %x", adb.CommandOpen, decoded.Command())
	}
	if decoded.DataString() != "shell:" {
		t.Fatalf("expected data %q, got %q", "shell:", decoded.DataString())
	}
}

func TestMessagesChannelDeliversIncomingMessages(t *testing.T) {
	client, server := newConnectedTestClient(t)

	outgoing := protocol.CreateTransporterMessage()
	outgoing.SetResponseCommand(protocol.CommandCreateRoom)
	if err := outgoing.SetPayloadCreateRoomResponse(&protocol.TransporterMessagePayloadCreateRoomResponse{RoomId: "ROOM9"}); err != nil {
		t.Fatalf("SetPayloadCreateRoomResponse failed: %s", err)
	}
	if err := outgoing.Write(server); err != nil {
		t.Fatalf("failed to write from the server: %s", err)
	}

	select {
	case container := <-client.Messages():
		message, err := container.Data()
		if err != nil {
			t.Fatalf("Data() failed: %s", err)
		}
		if message.Command() != protocol.CommandCreateRoom|protocol.CommandResponseMask {
			t.Fatalf("expected command %x, got %x", protocol.CommandCreateRoom|protocol.CommandResponseMask, message.Command())
		}
		payload, err := message.GetPayloadCreateRoomResponse()
		if err != nil {
			t.Fatalf("GetPayloadCreateRoomResponse failed: %s", err)
		}
		if payload.RoomId != "ROOM9" {
			t.Fatalf("expected room id %q, got %q", "ROOM9", payload.RoomId)
		}
		if err := container.Dispose(); err != nil {
			t.Fatalf("Dispose failed: %s", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the incoming message")
	}
}

func TestCloseStopsReaderWithoutPanicking(t *testing.T) {
	client, _ := newConnectedTestClient(t)
	client.Close()
	// Give the reader goroutine a chance to observe the cancellation; the
	// real assertion is that this does not panic or deadlock.
	time.Sleep(50 * time.Millisecond)
}

// TestMessagesChannelClosesOnDisconnect is a regression test: consumers of
// Messages() (the controller and relay packages) rely on the channel
// closing to notice the connection is gone, rather than blocking forever.
func TestMessagesChannelClosesOnDisconnect(t *testing.T) {
	client, server := newConnectedTestClient(t)
	_ = server.Close()

	select {
	case _, ok := <-client.Messages():
		if ok {
			t.Fatalf("expected the channel to be closed, got a value instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the channel to close after the server disconnected")
	}
}

// TestMessagesChannelClosesOnClientClose covers the other trigger for the
// same guarantee: calling Close() locally must also close the channel, not
// just a disconnect initiated by the peer.
func TestMessagesChannelClosesOnClientClose(t *testing.T) {
	client, _ := newConnectedTestClient(t)
	client.Close()

	select {
	case _, ok := <-client.Messages():
		if ok {
			t.Fatalf("expected the channel to be closed, got a value instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the channel to close after Close()")
	}
}

// TestConcurrentSendAdbMessageDoesNotInterleaveWrites exercises the
// scenario the owner-side stream multiplexer relies on: many goroutines
// (one per open ADB stream) calling SendAdbMessage concurrently must not
// interleave their bytes on the wire, or the transporter would see
// corrupted framing.
func TestConcurrentSendAdbMessageDoesNotInterleaveWrites(t *testing.T) {
	client, server := newConnectedTestClient(t)

	const senders = 20
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			adbMessage := adb.CreateMessage()
			if err := adbMessage.Set(adb.CommandOpen, uint32(i+1), 0, []byte(fmt.Sprintf("stream-%02d", i))); err != nil {
				t.Errorf("Set failed: %s", err)
				return
			}
			if err := client.SendAdbMessage(adbMessage); err != nil {
				t.Errorf("SendAdbMessage failed: %s", err)
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[uint32]bool)
	for i := 0; i < senders; i++ {
		received := readMessage(t, server)
		if received.Command() != protocol.CommandAdbTransport {
			t.Fatalf("expected command %x, got %x", protocol.CommandAdbTransport, received.Command())
		}
		decoded, err := adb.DecodeMessage(received.Payload())
		if err != nil {
			t.Fatalf("message #%d: DecodeMessage failed (interleaved/corrupted write?): %s", i, err)
		}
		expected := fmt.Sprintf("stream-%02d", decoded.Arg1()-1)
		if decoded.DataString() != expected {
			t.Fatalf("message #%d: expected data %q for arg1 %d, got %q", i, expected, decoded.Arg1(), decoded.DataString())
		}
		if seen[decoded.Arg1()] {
			t.Fatalf("message #%d: arg1 %d delivered more than once", i, decoded.Arg1())
		}
		seen[decoded.Arg1()] = true
	}
	if len(seen) != senders {
		t.Fatalf("expected %d distinct messages, got %d", senders, len(seen))
	}
}

func readMessage(t *testing.T, conn net.Conn) *protocol.TransporterMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	message := protocol.CreateTransporterMessage()
	if err := message.Read(conn); err != nil {
		t.Fatalf("failed to read a message: %s", err)
	}
	return message
}
