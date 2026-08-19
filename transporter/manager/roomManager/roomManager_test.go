package roomManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/config"
	"adb-remote.maci.team/transporter/manager/connectionManager"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func freeLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %s", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

// startTestSystem wires a ConnectionManager and RoomManager together exactly
// like the real transporter binary does, on an ephemeral local port.
func startTestSystem(t *testing.T) string {
	t.Helper()
	address := freeLocalAddress(t)
	dir := t.TempDir()
	cm := connectionManager.CreateConnectionManager(&config.TransporterConfiguration{
		Address:     address,
		TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "key.pem"),
	}, newTestLogger())
	rm := CreateRoomManager(cm, newTestLogger())

	started := make(chan struct{})
	go func() {
		go func() { _ = cm.StartServer() }()
		for i := 0; i < 100; i++ {
			if conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond); err == nil {
				_ = conn.Close()
				close(started)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(started)
	}()
	<-started

	t.Cleanup(func() {
		rm.Stop()
		cm.Stop()
	})
	return address
}

// testClient is a minimal raw-protocol client used to drive the transporter
// from the outside, the same way the real client/transportLayer.Client does.
type testClient struct {
	t        *testing.T
	conn     net.Conn
	clientId string
}

func dialTestClient(t *testing.T, address string) *testClient {
	t.Helper()
	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("failed to dial the server: %s", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	tc := &testClient{t: t, conn: conn}
	tc.connect()
	return tc
}

func (tc *testClient) connect() {
	tc.t.Helper()
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandConnect)
	if err := request.SetPayloadConnect(&protocol.TransporterMessagePayloadConnect{ProtocolVersion: protocol.ProtocolVersion}); err != nil {
		tc.t.Fatalf("SetPayloadConnect failed: %s", err)
	}
	if err := request.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the CNXN request: %s", err)
	}
	response := tc.readMessage()
	payload, err := response.GetPayloadConnectResponse()
	if err != nil {
		tc.t.Fatalf("GetPayloadConnectResponse failed: %s", err)
	}
	tc.clientId = payload.ClientId
}

func (tc *testClient) readMessage() *protocol.TransporterMessage {
	tc.t.Helper()
	_ = tc.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	message := protocol.CreateTransporterMessage()
	if err := message.Read(tc.conn); err != nil {
		tc.t.Fatalf("failed to read a message: %s", err)
	}
	return message
}

func (tc *testClient) createRoom() string {
	tc.t.Helper()
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandCreateRoom)
	if err := request.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the create-room request: %s", err)
	}
	response := tc.readMessage()
	if response.IsError() {
		payload, _ := response.GetErrorPayload()
		tc.t.Fatalf("create room failed: %+v", payload)
	}
	payload, err := response.GetPayloadCreateRoomResponse()
	if err != nil {
		tc.t.Fatalf("GetPayloadCreateRoomResponse failed: %s", err)
	}
	return payload.RoomId
}

func (tc *testClient) joinRoom(roomId string) {
	tc.t.Helper()
	tc.joinRoomWithKey(roomId, nil)
}

func (tc *testClient) joinRoomWithKey(roomId string, publicKey []byte) {
	tc.t.Helper()
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandJoinRoom)
	if err := request.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{RoomId: roomId, PublicKey: publicKey}); err != nil {
		tc.t.Fatalf("SetPayloadConnectRoom failed: %s", err)
	}
	if err := request.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the join-room request: %s", err)
	}
}

func (tc *testClient) expectJoinRoomRequest() (roomId string, guestClientId string) {
	tc.t.Helper()
	roomId, guestClientId, _ = tc.expectJoinRoomRequestWithKey()
	return roomId, guestClientId
}

func (tc *testClient) expectJoinRoomRequestWithKey() (roomId string, guestClientId string, guestPublicKey []byte) {
	tc.t.Helper()
	message := tc.readMessage()
	if message.Command() != protocol.CommandJoinRoom {
		tc.t.Fatalf("expected a join room request, got %x", message.Command())
	}
	payload, err := message.GetPayloadConnectRoom()
	if err != nil {
		tc.t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}
	return payload.RoomId, payload.ClientId, payload.PublicKey
}

func (tc *testClient) respondToJoinRoom(accepted int) {
	tc.t.Helper()
	tc.respondToJoinRoomWithKey(accepted, nil)
}

func (tc *testClient) respondToJoinRoomWithKey(accepted int, ownerPublicKey []byte) {
	tc.t.Helper()
	response := protocol.CreateTransporterMessage()
	response.SetResponseCommand(protocol.CommandJoinRoom)
	if err := response.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{Accepted: accepted, PublicKey: ownerPublicKey}); err != nil {
		tc.t.Fatalf("SetPayloadConnectRoomResult failed: %s", err)
	}
	if err := response.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the join-room response: %s", err)
	}
}

func (tc *testClient) expectJoinRoomResponse() int {
	tc.t.Helper()
	accepted, _, _ := tc.expectJoinRoomResponseWithKey()
	return accepted
}

func (tc *testClient) expectJoinRoomResponseWithKey() (accepted int, ownerClientId string, ownerPublicKey []byte) {
	tc.t.Helper()
	message := tc.readMessage()
	if message.Command() != protocol.CommandJoinRoom|protocol.CommandResponseMask {
		tc.t.Fatalf("expected a join room response, got %x", message.Command())
	}
	payload, err := message.GetPayloadConnectRoomResponse()
	if err != nil {
		tc.t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	return payload.Accepted, payload.ClientId, payload.PublicKey
}

func (tc *testClient) sendAdbTransport(raw []byte) {
	tc.t.Helper()
	message := protocol.CreateTransporterMessage()
	message.SetDirectCommand(protocol.CommandAdbTransport)
	if err := message.SetRawPayload(raw); err != nil {
		tc.t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := message.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the adb transport message: %s", err)
	}
}

func (tc *testClient) expectAdbTransport() []byte {
	tc.t.Helper()
	message := tc.readMessage()
	if message.Command() != protocol.CommandAdbTransport {
		tc.t.Fatalf("expected an adb transport message, got %x", message.Command())
	}
	return append([]byte{}, message.Payload()...)
}

func joinRoomAndAccept(t *testing.T, owner *testClient, guest *testClient, roomId string) {
	t.Helper()
	guest.joinRoom(roomId)
	_, guestClientId := owner.expectJoinRoomRequest()
	if guestClientId != guest.clientId {
		t.Fatalf("expected the guest's client id %q in the join request, got %q", guest.clientId, guestClientId)
	}
	owner.respondToJoinRoom(1)
	if accepted := guest.expectJoinRoomResponse(); accepted != 1 {
		t.Fatalf("expected the join room request to be accepted")
	}
}

func TestCreateAndJoinRoomAccepted(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	joinRoomAndAccept(t, owner, guest, roomId)
}

// TestJoinRoomForwardsIdentitiesBothWays confirms the transporter carries
// each side's public key to the other: the guest's key arrives with the
// join request (already covered by joinRoomAndAccept's guestClientId
// check), and the owner's key arrives with the join response, tagged with
// the owner's client id (which the owner itself never sends — the
// transporter fills it in from the connection it already knows).
func TestJoinRoomForwardsIdentitiesBothWays(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()

	guestPublicKey := []byte{0x01, 0x02, 0x03}
	guest.joinRoomWithKey(roomId, guestPublicKey)
	_, guestClientId, receivedGuestKey := owner.expectJoinRoomRequestWithKey()
	if guestClientId != guest.clientId {
		t.Fatalf("expected the guest's client id %q in the join request, got %q", guest.clientId, guestClientId)
	}
	if string(receivedGuestKey) != string(guestPublicKey) {
		t.Fatalf("expected the owner to receive the guest's public key %x, got %x", guestPublicKey, receivedGuestKey)
	}

	ownerPublicKey := []byte{0xaa, 0xbb, 0xcc}
	owner.respondToJoinRoomWithKey(1, ownerPublicKey)
	accepted, ownerClientId, receivedOwnerKey := guest.expectJoinRoomResponseWithKey()
	if accepted != 1 {
		t.Fatalf("expected the join request to be accepted")
	}
	if ownerClientId != owner.clientId {
		t.Fatalf("expected the guest to receive the owner's client id %q, got %q", owner.clientId, ownerClientId)
	}
	if string(receivedOwnerKey) != string(ownerPublicKey) {
		t.Fatalf("expected the guest to receive the owner's public key %x, got %x", ownerPublicKey, receivedOwnerKey)
	}
}

// TestSecondGuestIsRejected is a regression test for the invariant that a
// room has exactly one owner and one guest: a second guest trying to join
// an already-occupied room must be rejected, not silently replace the
// first guest.
func TestSecondGuestIsRejected(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest1 := dialTestClient(t, address)
	guest2 := dialTestClient(t, address)

	roomId := owner.createRoom()
	joinRoomAndAccept(t, owner, guest1, roomId)

	guest2.joinRoom(roomId)
	response := guest2.readMessage()
	if !response.IsError() {
		t.Fatalf("expected an error response for a second guest joining an occupied room")
	}
	payload, err := response.GetErrorPayload()
	if err != nil {
		t.Fatalf("GetErrorPayload failed: %s", err)
	}
	if payload.ErrorCode != protocol.ErrorFull {
		t.Fatalf("expected error code %d, got %d", protocol.ErrorFull, payload.ErrorCode)
	}

	// The first guest's room membership must be unaffected.
	guestToOwner := []byte("still connected")
	guest1.sendAdbTransport(guestToOwner)
	if received := owner.expectAdbTransport(); string(received) != string(guestToOwner) {
		t.Fatalf("expected the first guest to remain in the room, got %q", received)
	}
}

func TestJoinRoomDeclined(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	guest.joinRoom(roomId)
	owner.expectJoinRoomRequest()
	owner.respondToJoinRoom(0)
	if accepted := guest.expectJoinRoomResponse(); accepted != 0 {
		t.Fatalf("expected the join room request to be declined")
	}
}

func TestJoinRoomNotFound(t *testing.T) {
	address := startTestSystem(t)
	guest := dialTestClient(t, address)

	guest.joinRoom("does-not-exist")
	response := guest.readMessage()
	if !response.IsError() {
		t.Fatalf("expected an error response for an unknown room")
	}
	payload, err := response.GetErrorPayload()
	if err != nil {
		t.Fatalf("GetErrorPayload failed: %s", err)
	}
	if payload.ErrorCode != protocol.ErrorRoomNotFound {
		t.Fatalf("expected error code %d, got %d", protocol.ErrorRoomNotFound, payload.ErrorCode)
	}
}

// TestAdbTransportIsRelayedBetweenRoomParticipants exercises the core
// missing feature: once a room is established, opaque ADB transport
// messages sent by either participant must be forwarded to the other.
func TestAdbTransportIsRelayedBetweenRoomParticipants(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	joinRoomAndAccept(t, owner, guest, roomId)

	guestToOwner := []byte("guest->owner adb bytes")
	guest.sendAdbTransport(guestToOwner)
	if received := owner.expectAdbTransport(); string(received) != string(guestToOwner) {
		t.Fatalf("expected owner to receive %q, got %q", guestToOwner, received)
	}

	ownerToGuest := []byte("owner->guest adb bytes")
	owner.sendAdbTransport(ownerToGuest)
	if received := guest.expectAdbTransport(); string(received) != string(ownerToGuest) {
		t.Fatalf("expected guest to receive %q, got %q", ownerToGuest, received)
	}
}

func TestAdbTransportOutsideRoomIsDropped(t *testing.T) {
	address := startTestSystem(t)
	lonely := dialTestClient(t, address)

	lonely.sendAdbTransport([]byte("nobody listening"))

	_ = lonely.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	message := protocol.CreateTransporterMessage()
	err := message.Read(lonely.conn)
	if err == nil {
		t.Fatalf("did not expect any message to be relayed back to a client outside of a room")
	}
}

func TestOwnerDisconnectClosesGuestConnection(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	joinRoomAndAccept(t, owner, guest, roomId)

	_ = owner.conn.Close()

	buffer := make([]byte, 1)
	_ = guest.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := guest.conn.Read(buffer); err != io.EOF {
		t.Fatalf("expected the guest connection to be closed when the room owner disconnects, got err=%v", err)
	}
}

// TestGuestDisconnectNotifiesOwner is a regression test for the owner
// having no other way to learn its guest is gone: unlike the owner
// disconnecting (which the transporter tears the whole room down for), the
// owner's own connection survives a guest disconnect, so it needs an
// explicit CommandGuestLeft notification.
func TestGuestDisconnectNotifiesOwner(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	joinRoomAndAccept(t, owner, guest, roomId)

	_ = guest.conn.Close()

	message := owner.readMessage()
	if message.Command() != protocol.CommandGuestLeft {
		t.Fatalf("expected a CommandGuestLeft notification, got %x", message.Command())
	}

	// The owner's own connection must be unaffected: a fresh guest can
	// still join the now-empty room.
	guest2 := dialTestClient(t, address)
	joinRoomAndAccept(t, owner, guest2, roomId)
}
