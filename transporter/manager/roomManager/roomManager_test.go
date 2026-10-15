package roomManager

import (
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/transporter/config"
	"adb-remote.maci.team/transporter/manager/connectionManager"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
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
	return startTestSystemWith(t, func(*config.TransporterConfiguration) {})
}

// startTestSystemWith is startTestSystem with a configuration configure may
// adjust (e.g. lower a limit) before it starts.
func startTestSystemWith(t *testing.T, configure func(*config.TransporterConfiguration)) string {
	t.Helper()
	return startLoggingTestSystem(t, configure, newTestLogger())
}

// startLoggingTestSystem is startTestSystemWith logging to logger.
func startLoggingTestSystem(t *testing.T, configure func(*config.TransporterConfiguration), logger *slog.Logger) string {
	t.Helper()
	address := freeLocalAddress(t)
	dir := t.TempDir()
	transporterConfig := &config.TransporterConfiguration{
		Address:     address,
		TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "key.pem"),
	}
	configure(transporterConfig)
	cm := connectionManager.CreateConnectionManager(transporterConfig, logger)
	rm := CreateRoomManager(cm, transporterConfig, logger)

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

func testKey(size int, value byte) []byte {
	return bytes.Repeat([]byte{value}, size)
}

// testJoinRequest returns a join request whose keys have the right sizes;
// the transporter only checks their sizes, the owner checks the rest.
func testJoinRequest(roomId string) *protocol.TransporterMessagePayloadConnectRoom {
	return &protocol.TransporterMessagePayloadConnectRoom{
		RoomId:       roomId,
		PublicKey:    testKey(protocol.IdentityKeySize, 0x01),
		EphemeralKey: testKey(protocol.EphemeralKeySize, 0x02),
		Signature:    testKey(protocol.SignatureSize, 0x03),
	}
}

func (tc *testClient) joinRoom(roomId string) {
	tc.t.Helper()
	tc.sendJoinRequest(testJoinRequest(roomId))
}

func (tc *testClient) sendJoinRequest(request *protocol.TransporterMessagePayloadConnectRoom) {
	tc.t.Helper()
	message := protocol.CreateTransporterMessage()
	message.SetDirectCommand(protocol.CommandJoinRoom)
	if err := message.SetPayloadConnectRoom(request); err != nil {
		tc.t.Fatalf("SetPayloadConnectRoom failed: %s", err)
	}
	if err := message.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the join-room request: %s", err)
	}
}

func (tc *testClient) expectJoinRoomRequest() (roomId string, guestClientId string) {
	tc.t.Helper()
	request := tc.expectJoinRoomRequestPayload()
	return request.RoomId, request.ClientId
}

func (tc *testClient) expectJoinRoomRequestPayload() *protocol.TransporterMessagePayloadConnectRoom {
	tc.t.Helper()
	message := tc.readMessage()
	if message.Command() != protocol.CommandJoinRoom {
		tc.t.Fatalf("expected a join room request, got %x", message.Command())
	}
	payload, err := message.GetPayloadConnectRoom()
	if err != nil {
		tc.t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}
	return payload
}

// testJoinResult returns the owner's decision about guestClientId, with
// right-sized keys (and a key exchange, when accepting).
func testJoinResult(guestClientId string, accepted int) *protocol.TransporterMessagePayloadConnectRoomResult {
	result := &protocol.TransporterMessagePayloadConnectRoomResult{
		Accepted:  accepted,
		ClientId:  guestClientId,
		PublicKey: testKey(protocol.IdentityKeySize, 0x0a),
	}
	if accepted != 0 {
		result.EphemeralKey = testKey(protocol.EphemeralKeySize, 0x0b)
		result.Signature = testKey(protocol.SignatureSize, 0x0c)
	}
	return result
}

func (tc *testClient) respondToJoinRoom(guestClientId string, accepted int) {
	tc.t.Helper()
	tc.sendJoinResult(testJoinResult(guestClientId, accepted))
}

func (tc *testClient) sendJoinResult(result *protocol.TransporterMessagePayloadConnectRoomResult) {
	tc.t.Helper()
	response := protocol.CreateTransporterMessage()
	response.SetResponseCommand(protocol.CommandJoinRoom)
	if err := response.SetPayloadConnectRoomResult(result); err != nil {
		tc.t.Fatalf("SetPayloadConnectRoomResult failed: %s", err)
	}
	if err := response.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the join-room response: %s", err)
	}
}

func (tc *testClient) expectJoinRoomResponse() int {
	tc.t.Helper()
	return tc.expectJoinRoomResponsePayload().Accepted
}

func (tc *testClient) expectJoinRoomResponsePayload() *protocol.TransporterMessagePayloadConnectRoomResult {
	tc.t.Helper()
	message := tc.readMessage()
	if message.Command() != protocol.CommandJoinRoom|protocol.CommandResponseMask {
		tc.t.Fatalf("expected a join room response, got %x", message.Command())
	}
	payload, err := message.GetPayloadConnectRoomResponse()
	if err != nil {
		tc.t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	return payload
}

// expectError reads the next message and fails unless it is an error
// response carrying errorCode.
func (tc *testClient) expectError(errorCode int) {
	tc.t.Helper()
	message := tc.readMessage()
	if !message.IsError() {
		tc.t.Fatalf("expected an error response, got command %x", message.Command())
	}
	payload, err := message.GetErrorPayload()
	if err != nil {
		tc.t.Fatalf("GetErrorPayload failed: %s", err)
	}
	if payload.ErrorCode != errorCode {
		tc.t.Fatalf("expected error code %d, got %d (%s)", errorCode, payload.ErrorCode, payload.ErrorMessage)
	}
}

// expectNothing fails if any message arrives within a short while.
func (tc *testClient) expectNothing() {
	tc.t.Helper()
	_ = tc.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	message := protocol.CreateTransporterMessage()
	if err := message.Read(tc.conn); err == nil {
		tc.t.Fatalf("expected no message, got command %x", message.Command())
	}
}

// expectClosed fails unless the transporter closes the connection soon.
func (tc *testClient) expectClosed() {
	tc.t.Helper()
	_ = tc.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.Copy(io.Discard, tc.conn); err != nil {
		tc.t.Fatalf("expected the transporter to close the connection, got %s", err)
	}
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
	owner.respondToJoinRoom(guestClientId, 1)
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
// each side's public key and key exchange to the other: the guest's arrive
// with the join request, tagged with the guest's client id, and the
// owner's with the join response, tagged with the owner's client id (which
// the owner itself never sends — the transporter fills it in from the
// connection it already knows).
func TestJoinRoomForwardsIdentitiesBothWays(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()

	sentRequest := testJoinRequest(roomId)
	guest.sendJoinRequest(sentRequest)
	request := owner.expectJoinRoomRequestPayload()
	if request.ClientId != guest.clientId {
		t.Fatalf("expected the guest's client id %q in the join request, got %q", guest.clientId, request.ClientId)
	}
	if !bytes.Equal(request.PublicKey, sentRequest.PublicKey) || !bytes.Equal(request.EphemeralKey, sentRequest.EphemeralKey) || !bytes.Equal(request.Signature, sentRequest.Signature) {
		t.Fatalf("expected the owner to receive the guest's keys unchanged, got %+v", request)
	}

	sentResult := testJoinResult(request.ClientId, 1)
	owner.sendJoinResult(sentResult)
	result := guest.expectJoinRoomResponsePayload()
	if result.Accepted != 1 {
		t.Fatalf("expected the join request to be accepted")
	}
	if result.ClientId != owner.clientId {
		t.Fatalf("expected the guest to receive the owner's client id %q, got %q", owner.clientId, result.ClientId)
	}
	if !bytes.Equal(result.PublicKey, sentResult.PublicKey) || !bytes.Equal(result.EphemeralKey, sentResult.EphemeralKey) || !bytes.Equal(result.Signature, sentResult.Signature) {
		t.Fatalf("expected the guest to receive the owner's keys unchanged, got %+v", result)
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
	_, guestClientId := owner.expectJoinRoomRequest()
	owner.respondToJoinRoom(guestClientId, 0)
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

// TestMalformedJoinRoomLengthDoesNotCrashTransporter is a regression test
// for a real bug: RoomId's attacker-controlled string length field could
// overflow uint32 arithmetic in readString and panic parsing the message.
// Since RoomManager.dispatchMessage runs on a single goroutine shared by
// every room and client, an unrecovered panic there would have crashed the
// transporter for everyone (not just the sender) — if this test's process
// survives to reach the final assertion at all, the crash half of that bug
// is gone; the payload-level regression test lives in
// shared/protocol/payload.definition_test.go.
func TestMalformedJoinRoomLengthDoesNotCrashTransporter(t *testing.T) {
	address := startTestSystem(t)
	attacker := dialTestClient(t, address)

	malicious := protocol.CreateTransporterMessage()
	malicious.SetDirectCommand(protocol.CommandJoinRoom)
	raw := make([]byte, 8)
	protocol.ByteOrder.PutUint32(raw[0:4], 0xFFFFFFFC) // RoomId length: dataOffset(4)+this overflows to 0
	if err := malicious.SetRawPayload(raw); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if err := malicious.Write(attacker.conn); err != nil {
		t.Fatalf("failed to write the malicious message: %s", err)
	}

	response := attacker.readMessage()
	if !response.IsError() {
		t.Fatalf("expected an invalid-payload error response, got command %x", response.Command())
	}
	payload, err := response.GetErrorPayload()
	if err != nil {
		t.Fatalf("GetErrorPayload failed: %s", err)
	}
	if payload.ErrorCode != protocol.ErrorInvalidPayload {
		t.Fatalf("expected error code %d, got %d", protocol.ErrorInvalidPayload, payload.ErrorCode)
	}

	// The transporter itself must have survived: a fresh, well-behaved pair
	// of clients can still use it normally.
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)
	roomId := owner.createRoom()
	joinRoomAndAccept(t, owner, guest, roomId)
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

// TestPendingGuestCannotRelayAdbTraffic is a regression test for a guest
// reaching the owner's device before the owner decided anything: ADB
// traffic only flows once the owner accepted the guest.
func TestPendingGuestCannotRelayAdbTraffic(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	guest.joinRoom(roomId)
	_, guestClientId := owner.expectJoinRoomRequest()

	guest.sendAdbTransport([]byte("before the decision"))
	owner.expectNothing()

	owner.respondToJoinRoom(guestClientId, 1)
	guest.expectJoinRoomResponse()
	guest.sendAdbTransport([]byte("after the decision"))
	if received := owner.expectAdbTransport(); string(received) != "after the decision" {
		t.Fatalf("expected only the traffic sent after acceptance, got %q", received)
	}
}

// TestDeclinedGuestIsDisconnected checks a declined guest can't just keep
// its connection and ask again and again.
func TestDeclinedGuestIsDisconnected(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	guest.joinRoom(roomId)
	_, guestClientId := owner.expectJoinRoomRequest()
	owner.respondToJoinRoom(guestClientId, 0)
	if accepted := guest.expectJoinRoomResponse(); accepted != 0 {
		t.Fatalf("expected the join request to be declined")
	}
	guest.expectClosed()

	// The room itself stays open for someone else.
	joinRoomAndAccept(t, owner, dialTestClient(t, address), roomId)
}

// TestMalformedKeysAreRejectedWithoutClosingTheRoom is a regression test
// for a join request whose oversized public key used to make the
// transporter close the owner's room.
func TestMalformedKeysAreRejectedWithoutClosingTheRoom(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	attacker := dialTestClient(t, address)

	roomId := owner.createRoom()
	request := testJoinRequest(roomId)
	request.PublicKey = testKey(60000, 0x01)
	attacker.sendJoinRequest(request)
	attacker.expectError(protocol.ErrorInvalidPayload)
	owner.expectNothing()

	joinRoomAndAccept(t, owner, dialTestClient(t, address), roomId)
}

// TestMalformedOwnerKeysAreRejected covers the owner's side of the same
// check: a decision whose keys don't have their sizes isn't forwarded.
func TestMalformedOwnerKeysAreRejected(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomId := owner.createRoom()
	guest.joinRoom(roomId)
	_, guestClientId := owner.expectJoinRoomRequest()
	result := testJoinResult(guestClientId, 1)
	result.Signature = nil
	owner.sendJoinResult(result)
	owner.expectError(protocol.ErrorInvalidPayload)
	guest.expectNothing()
}

// TestClientCannotJoinASecondRoom is a regression test for one connection
// becoming the guest of several rooms at once.
func TestClientCannotJoinASecondRoom(t *testing.T) {
	address := startTestSystem(t)
	ownerA := dialTestClient(t, address)
	ownerB := dialTestClient(t, address)
	guest := dialTestClient(t, address)

	roomA := ownerA.createRoom()
	roomB := ownerB.createRoom()
	joinRoomAndAccept(t, ownerA, guest, roomA)

	guest.joinRoom(roomB)
	guest.expectError(protocol.ErrorAlreadyInRoom)
	ownerB.expectNothing()

	ownerA.joinRoom(roomB)
	ownerA.expectError(protocol.ErrorAlreadyInRoom)
	ownerB.expectNothing()
}

// TestDecisionAboutADepartedGuestIsRefused is a regression test for an
// accept landing on whichever guest happens to be in the room when it
// arrives: the decision names the guest it is about.
func TestDecisionAboutADepartedGuestIsRefused(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	guest1 := dialTestClient(t, address)
	guest2 := dialTestClient(t, address)

	roomId := owner.createRoom()
	guest1.joinRoom(roomId)
	owner.expectJoinRoomRequest()
	_ = guest1.conn.Close()
	if message := owner.readMessage(); message.Command() != protocol.CommandGuestLeft {
		t.Fatalf("expected GUEST_LEFT, got %x", message.Command())
	}

	guest2.joinRoom(roomId)
	owner.expectJoinRoomRequest()
	owner.respondToJoinRoom(guest1.clientId, 1)
	owner.expectError(protocol.ErrorNoParticipant)
	guest2.expectNothing()

	owner.respondToJoinRoom(guest2.clientId, 1)
	if accepted := guest2.expectJoinRoomResponse(); accepted != 1 {
		t.Fatalf("expected the decision about guest2 to reach it")
	}
}

func (tc *testClient) createRoomExpectingError(errorCode int) {
	tc.t.Helper()
	request := protocol.CreateTransporterMessage()
	request.SetDirectCommand(protocol.CommandCreateRoom)
	request.ClearPayload()
	if err := request.Write(tc.conn); err != nil {
		tc.t.Fatalf("failed to write the create-room request: %s", err)
	}
	tc.expectError(errorCode)
}

func TestRoomsPerIPAreLimited(t *testing.T) {
	address := startTestSystemWith(t, func(c *config.TransporterConfiguration) {
		c.MaxRoomsPerIP = 1
	})
	dialTestClient(t, address).createRoom()
	dialTestClient(t, address).createRoomExpectingError(protocol.ErrorLimitReached)
}

func TestRoomsAreLimitedInTotal(t *testing.T) {
	address := startTestSystemWith(t, func(c *config.TransporterConfiguration) {
		c.MaxRooms = 1
	})
	first := dialTestClient(t, address)
	first.createRoom()
	dialTestClient(t, address).createRoomExpectingError(protocol.ErrorLimitReached)

	// Closing a room frees its place.
	_ = first.conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		candidate := dialTestClient(t, address)
		request := protocol.CreateTransporterMessage()
		request.SetDirectCommand(protocol.CommandCreateRoom)
		request.ClearPayload()
		if err := request.Write(candidate.conn); err != nil {
			t.Fatalf("failed to write the create-room request: %s", err)
		}
		if !candidate.readMessage().IsError() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected the closed room's place to be freed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRoomWithoutGuestExpires checks an abandoned room doesn't hold its
// place forever, and that the owner learns why it was closed.
func TestRoomWithoutGuestExpires(t *testing.T) {
	address := startTestSystemWith(t, func(c *config.TransporterConfiguration) {
		c.RoomIdleTimeout = "200ms"
	})
	owner := dialTestClient(t, address)
	owner.createRoom()

	owner.expectError(protocol.ErrorRoomExpired)
	owner.expectClosed()
}

func TestRoomWithAcceptedGuestDoesNotExpire(t *testing.T) {
	address := startTestSystemWith(t, func(c *config.TransporterConfiguration) {
		c.RoomIdleTimeout = "200ms"
	})
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)
	roomId := owner.createRoom()
	joinRoomAndAccept(t, owner, guest, roomId)

	time.Sleep(600 * time.Millisecond)
	guest.sendAdbTransport([]byte("still here"))
	if received := owner.expectAdbTransport(); string(received) != "still here" {
		t.Fatalf("expected the room to stay open, got %q", received)
	}
}

// TestFailedJoinsAreRateLimited checks a client can't keep guessing room
// ids: after too many misses from one address, even a real room id is
// refused for a while.
func TestFailedJoinsAreRateLimited(t *testing.T) {
	address := startTestSystem(t)
	owner := dialTestClient(t, address)
	roomId := owner.createRoom()
	attacker := dialTestClient(t, address)

	for i := 0; i < maxFailedJoinsPerWindow; i++ {
		attacker.joinRoom(fmt.Sprintf("NOPE-%04d", i))
		attacker.expectError(protocol.ErrorRoomNotFound)
	}
	attacker.joinRoom(roomId)
	attacker.expectError(protocol.ErrorTooManyAttempts)
	owner.expectNothing()
}

// TestNonReadingClientDoesNotStallOtherRooms is a regression test for one
// room's client that stops reading freezing every room: the transporter
// used to write to it from the one goroutine that serves all of them.
func TestNonReadingClientDoesNotStallOtherRooms(t *testing.T) {
	address := startTestSystem(t)
	ownerA := dialTestClient(t, address)
	guestA := dialTestClient(t, address)
	joinRoomAndAccept(t, ownerA, guestA, ownerA.createRoom())
	ownerB := dialTestClient(t, address)
	guestB := dialTestClient(t, address)
	joinRoomAndAccept(t, ownerB, guestB, ownerB.createRoom())

	// ownerA never reads; guestA floods it far past every buffer on the way.
	flood := protocol.CreateTransporterMessage()
	flood.SetDirectCommand(protocol.CommandAdbTransport)
	if err := flood.SetRawPayload(make([]byte, 60000)); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	go func() {
		for i := 0; i < 1000; i++ {
			if err := flood.Write(guestA.conn); err != nil {
				return
			}
		}
	}()

	for i := 0; i < 50; i++ {
		guestB.sendAdbTransport([]byte("room B"))
		if received := ownerB.expectAdbTransport(); string(received) != "room B" {
			t.Fatalf("expected room B to keep relaying, got %q", received)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// syncBuffer collects log output written from several goroutines.
type syncBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

// TestRelayingDoesNotLogEveryMessage keeps the log about connections and
// rooms: relaying traffic once a room is up writes nothing to it.
func TestRelayingDoesNotLogEveryMessage(t *testing.T) {
	logs := &syncBuffer{}
	address := startLoggingTestSystem(t, func(*config.TransporterConfiguration) {}, slog.New(slog.NewTextHandler(logs, nil)))
	owner := dialTestClient(t, address)
	guest := dialTestClient(t, address)
	joinRoomAndAccept(t, owner, guest, owner.createRoom())
	// The transporter goes back to reading right after relaying the last
	// message of the join; give it that moment before taking the baseline.
	time.Sleep(100 * time.Millisecond)
	before := logs.String()

	for i := 0; i < 20; i++ {
		guest.sendAdbTransport([]byte("guest->owner"))
		owner.expectAdbTransport()
		owner.sendAdbTransport([]byte("owner->guest"))
		guest.expectAdbTransport()
	}
	time.Sleep(100 * time.Millisecond)

	if logged := strings.TrimPrefix(logs.String(), before); logged != "" {
		t.Fatalf("expected relaying to log nothing, got:\n%s", logged)
	}
}
