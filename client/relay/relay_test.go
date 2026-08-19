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
