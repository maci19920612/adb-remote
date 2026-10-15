package relay

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/shared/utils"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeTransportClient is a minimal, in-memory TransportClient double: every
// SendAdbMessage call publishes a byte-copy snapshot (since the caller
// reuses its AdbMessage buffer) on the sent channel, and test code can push
// synthetic incoming TransporterMessages via deliver.
type fakeTransportClient struct {
	sent     chan []byte
	messages chan *transportLayer.MessageContainer
	pool     *utils.ObjectPool[protocol.TransporterMessage]
}

func newFakeTransportClient() *fakeTransportClient {
	factory := func() *protocol.TransporterMessage { return protocol.CreateTransporterMessage() }
	return &fakeTransportClient{
		sent:     make(chan []byte, 8),
		messages: make(chan *transportLayer.MessageContainer, 8),
		pool:     utils.NewObjectPool(factory),
	}
}

func (f *fakeTransportClient) SendAdbMessage(message *adb.AdbMessage) error {
	snapshot := append([]byte{}, message.Bytes()...)
	f.sent <- snapshot
	return nil
}

func (f *fakeTransportClient) Messages() <-chan *transportLayer.MessageContainer {
	return f.messages
}

// deliverAdbTransport wraps an ADB message as an incoming CommandAdbTransport
// TransporterMessage, as if it had arrived from the peer.
func (f *fakeTransportClient) deliverAdbTransport(t *testing.T, adbMessage *adb.AdbMessage) {
	t.Helper()
	container := f.pool.Obtain()
	message, err := container.Data()
	if err != nil {
		t.Fatalf("Data() failed: %s", err)
	}
	message.SetDirectCommand(protocol.CommandAdbTransport)
	if err := message.SetRawPayload(adbMessage.Bytes()); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	f.messages <- container
}

func (f *fakeTransportClient) deliverOther(t *testing.T, command uint32) {
	t.Helper()
	container := f.pool.Obtain()
	message, err := container.Data()
	if err != nil {
		t.Fatalf("Data() failed: %s", err)
	}
	message.SetDirectCommand(command)
	f.messages <- container
}

func TestRelayForwardsLocalMessagesToRemote(t *testing.T) {
	proxySide, localAdbServerSide := net.Pipe()
	client := newFakeTransportClient()
	logger := newTestLogger()

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), proxySide, client, logger) }()

	outgoing := adb.CreateMessage()
	if err := outgoing.Set(adb.CommandOpen, 1, 0, []byte("shell:")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := outgoing.Write(localAdbServerSide); err != nil {
		t.Fatalf("failed to write from the local side: %s", err)
	}

	select {
	case bytes := <-client.sent:
		decoded, err := adb.DecodeMessage(bytes)
		if err != nil {
			t.Fatalf("DecodeMessage failed: %s", err)
		}
		if decoded.Command() != adb.CommandOpen || decoded.DataString() != "shell:" {
			t.Fatalf("unexpected forwarded message: %+v", decoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the relay to forward the message")
	}

	_ = localAdbServerSide.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("relay did not stop after the local connection closed")
	}
}

func TestRelayForwardsRemoteMessagesToLocal(t *testing.T) {
	proxySide, localAdbServerSide := net.Pipe()
	client := newFakeTransportClient()
	logger := newTestLogger()

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), proxySide, client, logger) }()

	incoming := adb.CreateMessage()
	if err := incoming.Set(adb.CommandOkay, 2, 0, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, incoming)

	received := adb.CreateMessage()
	readErr := make(chan error, 1)
	go func() { readErr <- received.Read(localAdbServerSide) }()

	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("failed to read the relayed message: %s", err)
		}
		if received.Command() != adb.CommandOkay {
			t.Fatalf("expected command %x, got %x", adb.CommandOkay, received.Command())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the relayed message")
	}

	_ = localAdbServerSide.Close()
	<-done
}

func TestRelayIgnoresNonTransportMessagesAndKeepsRunning(t *testing.T) {
	proxySide, localAdbServerSide := net.Pipe()
	client := newFakeTransportClient()
	logger := newTestLogger()

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), proxySide, client, logger) }()

	// An unrelated control message should be ignored, not tear the relay down.
	client.deliverOther(t, protocol.CommandJoinRoom)

	incoming := adb.CreateMessage()
	if err := incoming.Set(adb.CommandClose, 0, 0, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, incoming)

	received := adb.CreateMessage()
	readErr := make(chan error, 1)
	go func() { readErr <- received.Read(localAdbServerSide) }()

	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("failed to read the relayed message: %s", err)
		}
		if received.Command() != adb.CommandClose {
			t.Fatalf("expected the CLOSE message to still be relayed after the ignored one, got %x", received.Command())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the relayed message")
	}

	_ = localAdbServerSide.Close()
	<-done
}

func TestRelayStopsOnContextCancellation(t *testing.T) {
	proxySide, localAdbServerSide := net.Pipe()
	defer localAdbServerSide.Close()
	client := newFakeTransportClient()
	logger := newTestLogger()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, proxySide, client, logger) }()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("relay did not stop after context cancellation")
	}
}

// startRelay runs Run between a pipe and a fake client until the test ends.
func startRelay(t *testing.T) (localAdbServerSide net.Conn, client *fakeTransportClient) {
	t.Helper()
	proxySide, localAdbServerSide := net.Pipe()
	client = newFakeTransportClient()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, proxySide, client, newTestLogger()) }()
	t.Cleanup(func() {
		cancel()
		_ = localAdbServerSide.Close()
		<-done
	})
	return localAdbServerSide, client
}

func sendFromLocal(t *testing.T, local net.Conn, client *fakeTransportClient, command uint32, arg1 uint32, arg2 uint32, data string) {
	t.Helper()
	message := adb.CreateMessage()
	if err := message.Set(command, arg1, arg2, []byte(data)); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if err := message.Write(local); err != nil {
		t.Fatalf("failed to write from the local side: %s", err)
	}
	select {
	case <-client.sent:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the relay to forward the local message")
	}
}

func deliverFromPeer(t *testing.T, client *fakeTransportClient, command uint32, arg1 uint32, arg2 uint32, data string) {
	t.Helper()
	message := adb.CreateMessage()
	if err := message.Set(command, arg1, arg2, []byte(data)); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	client.deliverAdbTransport(t, message)
}

func expectLocal(t *testing.T, local net.Conn) *adb.AdbMessage {
	t.Helper()
	received := adb.CreateMessage()
	_ = local.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := received.Read(local); err != nil {
		t.Fatalf("failed to read the relayed message: %s", err)
	}
	return received
}

// expectRefusedOpen fails unless the relay answered the peer's OPEN (with
// id peerId) with a CLSE, without relaying it.
func expectRefusedOpen(t *testing.T, client *fakeTransportClient, peerId uint32) {
	t.Helper()
	select {
	case raw := <-client.sent:
		decoded, err := adb.DecodeMessage(raw)
		if err != nil {
			t.Fatalf("DecodeMessage failed: %s", err)
		}
		if decoded.Command() != adb.CommandClose || decoded.Arg2() != peerId {
			t.Fatalf("expected a CLSE for the peer's stream %d, got command %x arg2 %d", peerId, decoded.Command(), decoded.Arg2())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the relay to refuse the peer's OPEN")
	}
}

// TestRelayRefusesUnsolicitedOpenFromThePeer is a regression test for a
// malicious room owner crashing the guest's adb-server: adb-server aborts
// when a device opens a stream to anything that isn't one of its reverse
// forwards, so the relay must only pass such OPENs for reverse forwards
// the local adb-server actually requested.
func TestRelayRefusesUnsolicitedOpenFromThePeer(t *testing.T) {
	local, client := startRelay(t)

	deliverFromPeer(t, client, adb.CommandOpen, 9, 0, "tcp:5037\x00")
	expectRefusedOpen(t, client, 9)

	// The relay keeps going: the next legitimate message still arrives.
	deliverFromPeer(t, client, adb.CommandOkay, 2, 1, "")
	if received := expectLocal(t, local); received.Command() != adb.CommandOkay {
		t.Fatalf("expected the OKAY after the refused OPEN, got %x", received.Command())
	}
}

func TestRelayPassesOpenForARequestedReverseForward(t *testing.T) {
	local, client := startRelay(t)

	sendFromLocal(t, local, client, adb.CommandOpen, 1, 0, "reverse:forward:localabstract:scrcpy;tcp:27183\x00")
	deliverFromPeer(t, client, adb.CommandOpen, 9, 0, "tcp:27183\x00")

	received := expectLocal(t, local)
	if received.Command() != adb.CommandOpen || received.DataString() != "tcp:27183\x00" {
		t.Fatalf("expected the OPEN for the reverse forward, got %x %q", received.Command(), received.DataString())
	}
}

func TestRelayForgetsRemovedReverseForwards(t *testing.T) {
	local, client := startRelay(t)

	sendFromLocal(t, local, client, adb.CommandOpen, 1, 0, "reverse:forward:localabstract:scrcpy;tcp:27183\x00")
	sendFromLocal(t, local, client, adb.CommandOpen, 2, 0, "reverse:killforward:localabstract:scrcpy\x00")
	deliverFromPeer(t, client, adb.CommandOpen, 9, 0, "tcp:27183\x00")
	expectRefusedOpen(t, client, 9)

	sendFromLocal(t, local, client, adb.CommandOpen, 3, 0, "reverse:forward:tcp:1;tcp:1111\x00")
	sendFromLocal(t, local, client, adb.CommandOpen, 4, 0, "reverse:killforward-all\x00")
	deliverFromPeer(t, client, adb.CommandOpen, 10, 0, "tcp:1111\x00")
	expectRefusedOpen(t, client, 10)
}

// TestRelayDropsConnectFromThePeer covers the peer trying to restart the
// local adb-server's connection handshake mid-session.
func TestRelayDropsConnectFromThePeer(t *testing.T) {
	local, client := startRelay(t)

	deliverFromPeer(t, client, adb.CommandConnect, 0x01000001, adb.MaxPayloadLength, "device::")
	deliverFromPeer(t, client, adb.CommandOkay, 2, 1, "")
	if received := expectLocal(t, local); received.Command() != adb.CommandOkay {
		t.Fatalf("expected the CNXN to be dropped and the OKAY relayed, got %x", received.Command())
	}
}
