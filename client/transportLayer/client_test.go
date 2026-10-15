package transportLayer

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/e2e"
	"adb-remote.maci.team/client/identity"
	"adb-remote.maci.team/client/internal/testtls"
	"adb-remote.maci.team/shared/certfingerprint"
	"adb-remote.maci.team/shared/protocol"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	offer := e2e.KeyExchange{EphemeralKey: []byte{0x04}, Signature: []byte{0x05}}
	if err := client.SendJoinRoom("ROOM7", publicKey, offer); err != nil {
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
	if !bytes.Equal(payload.EphemeralKey, offer.EphemeralKey) || !bytes.Equal(payload.Signature, offer.Signature) {
		t.Fatalf("expected the offer to go with the request, got %+v", payload)
	}
}

// TestSendJoinRoomResponseNamesTheGuest checks the owner's decision says
// which guest it is about, so the transporter can refuse it once that
// guest is gone.
func TestSendJoinRoomResponseNamesTheGuest(t *testing.T) {
	client, server := newConnectedTestClient(t)

	answer := e2e.KeyExchange{EphemeralKey: []byte{0x04}, Signature: []byte{0x05}}
	if err := client.SendJoinRoomResponse("GUEST1", 1, []byte{0x01}, answer); err != nil {
		t.Fatalf("SendJoinRoomResponse failed: %s", err)
	}

	payload, err := readMessage(t, server).GetPayloadConnectRoomResponse()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	if payload.Accepted != 1 || payload.ClientId != "GUEST1" {
		t.Fatalf("expected an acceptance of GUEST1, got %+v", payload)
	}
	if !bytes.Equal(payload.EphemeralKey, answer.EphemeralKey) || !bytes.Equal(payload.Signature, answer.Signature) {
		t.Fatalf("expected the answer to go with the decision, got %+v", payload)
	}
}

// TestSendCreateRoomCarriesNoPayload is a regression test for a pooled
// message going out with whatever its previous use left in it.
func TestSendCreateRoomCarriesNoPayload(t *testing.T) {
	client, server := newConnectedTestClient(t)

	if err := client.SendConnect(); err != nil {
		t.Fatalf("SendConnect failed: %s", err)
	}
	readMessage(t, server)
	if err := client.SendCreateRoom(); err != nil {
		t.Fatalf("SendCreateRoom failed: %s", err)
	}
	if received := readMessage(t, server); received.PayloadLength() != 0 {
		t.Fatalf("expected an empty create-room request, got %d payload bytes", received.PayloadLength())
	}
}

func testIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Load(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatalf("failed to create a test identity: %s", err)
	}
	return id
}

// newSessionPair runs a key exchange and returns the guest's and the
// owner's sessions.
func newSessionPair(t *testing.T) (guestSession *e2e.Session, ownerSession *e2e.Session) {
	t.Helper()
	guest := testIdentity(t)
	owner := testIdentity(t)
	handshake, err := e2e.StartGuest(guest, "ROOM7")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	answer, ownerSession, err := e2e.Accept(owner, "ROOM7", guest.PublicKey, handshake.Offer())
	if err != nil {
		t.Fatalf("Accept failed: %s", err)
	}
	guestSession, err = handshake.Finish(owner.PublicKey, answer)
	if err != nil {
		t.Fatalf("Finish failed: %s", err)
	}
	return guestSession, ownerSession
}

func TestSendAdbMessageEncryptsTheAdbMessage(t *testing.T) {
	client, server := newConnectedTestClient(t)
	clientSession, peerSession := newSessionPair(t)
	client.SetSession(clientSession)

	adbMessage := adb.CreateMessage()
	if err := adbMessage.Set(adb.CommandOpen, 1, 0, []byte("shell:id")); err != nil {
		t.Fatalf("adb Set failed: %s", err)
	}
	if err := client.SendAdbMessage(adbMessage); err != nil {
		t.Fatalf("SendAdbMessage failed: %s", err)
	}

	received := readMessage(t, server)
	if received.Command() != protocol.CommandAdbTransport {
		t.Fatalf("expected command %x, got %x", protocol.CommandAdbTransport, received.Command())
	}
	if bytes.Contains(received.Payload(), []byte("shell:id")) {
		t.Fatalf("expected the ADB message to be encrypted on the wire")
	}
	opened, err := peerSession.Open(nil, received.Payload())
	if err != nil {
		t.Fatalf("the peer failed to decrypt the message: %s", err)
	}
	decoded, err := adb.DecodeMessage(opened)
	if err != nil {
		t.Fatalf("DecodeMessage failed: %s", err)
	}
	if decoded.Command() != adb.CommandOpen || decoded.DataString() != "shell:id" {
		t.Fatalf("expected OPEN shell:id, got %x %q", decoded.Command(), decoded.DataString())
	}
}

// TestSendAdbMessageWithoutSessionFails checks ADB traffic never goes out
// unencrypted, e.g. before a guest was accepted.
func TestSendAdbMessageWithoutSessionFails(t *testing.T) {
	client, server := newConnectedTestClient(t)

	adbMessage := adb.CreateMessage()
	if err := adbMessage.Set(adb.CommandOpen, 1, 0, []byte("shell:")); err != nil {
		t.Fatalf("adb Set failed: %s", err)
	}
	if err := client.SendAdbMessage(adbMessage); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expected ErrNoSession, got %v", err)
	}
	_ = server.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if err := protocol.CreateTransporterMessage().Read(server); err == nil {
		t.Fatalf("expected nothing to be sent without a session")
	}
}

func sendSealed(t *testing.T, server net.Conn, session *e2e.Session, plaintext []byte) {
	t.Helper()
	message := protocol.CreateTransporterMessage()
	message.SetDirectCommand(protocol.CommandAdbTransport)
	if err := message.SetRawPayload(session.Seal(nil, plaintext)); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := message.Write(server); err != nil {
		t.Fatalf("failed to write from the server: %s", err)
	}
}

func expectIncoming(t *testing.T, client *Client) *protocol.TransporterMessage {
	t.Helper()
	select {
	case container, ok := <-client.Messages():
		if !ok {
			t.Fatalf("expected a message, the channel closed")
		}
		message, err := container.Data()
		if err != nil {
			t.Fatalf("Data() failed: %s", err)
		}
		copied := protocol.CreateTransporterMessage()
		copied.SetDirectCommand(message.Command())
		if err := copied.SetRawPayload(message.Payload()); err != nil {
			t.Fatalf("SetRawPayload failed: %s", err)
		}
		_ = container.Dispose()
		return copied
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for an incoming message")
		return nil
	}
}

func TestIncomingAdbTransportIsDecrypted(t *testing.T) {
	client, server := newConnectedTestClient(t)
	clientSession, peerSession := newSessionPair(t)
	client.SetSession(clientSession)

	for _, plaintext := range []string{"first", "second"} {
		sendSealed(t, server, peerSession, []byte(plaintext))
		if received := expectIncoming(t, client); string(received.Payload()) != plaintext {
			t.Fatalf("expected the decrypted %q, got %q", plaintext, received.Payload())
		}
	}
}

// TestIncomingAdbTransportWithoutSessionIsDropped checks nothing reaches
// the relay before a session exists, e.g. a guest that wasn't accepted.
func TestIncomingAdbTransportWithoutSessionIsDropped(t *testing.T) {
	client, server := newConnectedTestClient(t)

	unexpected := protocol.CreateTransporterMessage()
	unexpected.SetDirectCommand(protocol.CommandAdbTransport)
	if err := unexpected.SetRawPayload([]byte("not now")); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := unexpected.Write(server); err != nil {
		t.Fatalf("failed to write from the server: %s", err)
	}
	guestLeft := protocol.CreateTransporterMessage()
	guestLeft.SetDirectCommand(protocol.CommandGuestLeft)
	if err := guestLeft.Write(server); err != nil {
		t.Fatalf("failed to write from the server: %s", err)
	}

	if received := expectIncoming(t, client); received.Command() != protocol.CommandGuestLeft {
		t.Fatalf("expected the ADB message to be dropped, got command %x", received.Command())
	}
}

// TestUndecryptableAdbTransportClosesTheConnection checks a message that
// was tampered with ends the session rather than being skipped.
func TestUndecryptableAdbTransportClosesTheConnection(t *testing.T) {
	client, server := newConnectedTestClient(t)
	clientSession, _ := newSessionPair(t)
	client.SetSession(clientSession)
	_, otherSession := newSessionPair(t)

	sendSealed(t, server, otherSession, []byte("forged"))

	select {
	case container, ok := <-client.Messages():
		if ok {
			_ = container.Dispose()
			t.Fatalf("expected the channel to close instead of delivering a message")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the channel to close")
	}
}

// TestTLS12TransporterIsRefused checks the client insists on TLS 1.3.
func TestTLS12TransporterIsRefused(t *testing.T) {
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{testtls.Certificate(t)}, MaxVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("failed to start a TLS 1.2 listener: %s", err)
	}
	defer listener.Close()
	go func() { _, _ = testtls.Accept(listener) }()

	client, err := CreateClient(newTestLogger(), &config.ClientConfiguration{TransporterAddress: listener.Addr().String()})
	if err != nil {
		t.Fatalf("CreateClient failed: %s", err)
	}
	if err := client.Start(); err == nil {
		client.Close()
		t.Fatalf("expected the client to refuse a TLS 1.2 transporter")
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

// TestBytesSentAndReceivedTrackWireTraffic is a regression test for the
// transfer-stats footer's data source: BytesSent/BytesReceived must reflect
// the total wire size (header+payload) of every message actually
// written/read, not just payload bytes or message counts.
func TestBytesSentAndReceivedTrackWireTraffic(t *testing.T) {
	client, server := newConnectedTestClient(t)

	if client.BytesSent() != 0 || client.BytesReceived() != 0 {
		t.Fatalf("expected zero counters on a fresh client, got sent=%d received=%d", client.BytesSent(), client.BytesReceived())
	}

	if err := client.SendJoinRoom("ROOM7", []byte{1, 2, 3}, e2e.KeyExchange{}); err != nil {
		t.Fatalf("SendJoinRoom failed: %s", err)
	}
	sent := readMessage(t, server)
	wantSent := uint64(sent.WireSize())
	if got := client.BytesSent(); got != wantSent {
		t.Fatalf("expected BytesSent %d after one message, got %d", wantSent, got)
	}

	incoming := protocol.CreateTransporterMessage()
	incoming.SetResponseCommand(protocol.CommandJoinRoom)
	if err := incoming.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{Accepted: 1}); err != nil {
		t.Fatalf("SetPayloadConnectRoomResult failed: %s", err)
	}
	wantReceived := uint64(incoming.WireSize())
	if err := incoming.Write(server); err != nil {
		t.Fatalf("failed to write from the server: %s", err)
	}
	container := <-client.Messages()
	defer container.Dispose()

	if got := client.BytesReceived(); got != wantReceived {
		t.Fatalf("expected BytesReceived %d after one message, got %d", wantReceived, got)
	}
}

// TestEnableDebugCaptureRecordsBothDirections is a regression test for the
// debug-verbosity packet capture: once enabled, both outgoing and incoming
// messages must land in the pcap file, and the file must be readable
// (non-empty, past the global header) once the client is closed.
func TestEnableDebugCaptureRecordsBothDirections(t *testing.T) {
	client, server := newConnectedTestClient(t)

	capturePath := filepath.Join(t.TempDir(), "capture.pcap")
	if err := client.EnableDebugCapture(capturePath); err != nil {
		t.Fatalf("EnableDebugCapture failed: %s", err)
	}

	if err := client.SendConnect(); err != nil {
		t.Fatalf("SendConnect failed: %s", err)
	}
	readMessage(t, server)

	outgoing := protocol.CreateTransporterMessage()
	outgoing.SetResponseCommand(protocol.CommandConnect)
	if err := outgoing.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{ClientId: "ABCD1234"}); err != nil {
		t.Fatalf("SetPayloadConnectResponse failed: %s", err)
	}
	if err := outgoing.Write(server); err != nil {
		t.Fatalf("failed to write from the server: %s", err)
	}
	container := <-client.Messages()
	container.Dispose()

	client.Close()

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("failed to read the capture file: %s", err)
	}
	// 24 byte global header + at least two packet records (16 byte header
	// each, plus a non-empty frame) for the one outgoing and one incoming
	// message above.
	if len(data) <= 24+16+16 {
		t.Fatalf("expected the capture file to contain at least two packet records, got %d bytes", len(data))
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
	clientSession, peerSession := newSessionPair(t)
	client.SetSession(clientSession)

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
		// Opening in arrival order also checks every message went out in
		// the order it was sealed.
		opened, err := peerSession.Open(nil, received.Payload())
		if err != nil {
			t.Fatalf("message #%d: failed to decrypt (sealed and sent out of order?): %s", i, err)
		}
		decoded, err := adb.DecodeMessage(opened)
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

// listenWithCertificate stands in for a transporter presenting a known
// certificate, completing the server side of every TLS handshake.
func listenWithCertificate(t *testing.T) (address string, fingerprint string) {
	t.Helper()
	cert := testtls.Certificate(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("failed to start a TLS listener: %s", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := testtls.Accept(listener)
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	return listener.Addr().String(), certfingerprint.Of(cert.Certificate[0])
}

func startPinnedClient(t *testing.T, address string, pinnedFingerprint string) error {
	t.Helper()
	client, err := CreateClient(newTestLogger(), &config.ClientConfiguration{TransporterAddress: address, TransporterCertificateFingerprint: pinnedFingerprint})
	if err != nil {
		t.Fatalf("CreateClient failed: %s", err)
	}
	err = client.Start()
	if err == nil {
		client.Close()
	}
	return err
}

func TestPinnedCertificateIsAccepted(t *testing.T) {
	address, fingerprint := listenWithCertificate(t)
	if err := startPinnedClient(t, address, fingerprint); err != nil {
		t.Fatalf("expected the pinned certificate to be accepted, got %s", err)
	}
}

// TestOtherCertificateIsRefusedWhenPinned covers a man in the middle
// between the client and the transporter: with a pinned fingerprint, any
// other certificate fails the handshake.
func TestOtherCertificateIsRefusedWhenPinned(t *testing.T) {
	address, fingerprint := listenWithCertificate(t)
	_, otherFingerprint := listenWithCertificate(t)

	err := startPinnedClient(t, address, otherFingerprint)
	if err == nil {
		t.Fatalf("expected a certificate other than the pinned one to be refused")
	}
	if !strings.Contains(err.Error(), fingerprint) || !strings.Contains(err.Error(), otherFingerprint) {
		t.Fatalf("expected the error to name both the presented and the pinned fingerprint, got %s", err)
	}
}

func TestMalformedPinnedFingerprintIsRefused(t *testing.T) {
	address, _ := listenWithCertificate(t)
	if err := startPinnedClient(t, address, "SHA256:not-a-fingerprint"); err == nil {
		t.Fatalf("expected a malformed pinned fingerprint to stop the client")
	}
}
