package protocol

import (
	"bytes"
	"testing"
)

func TestErrorPayloadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	if err := m.SetErrorPayload(&TransporterMessagePayloadError{
		ErrorCode:    ErrorRoomNotFound,
		ErrorMessage: "room not found",
	}); err != nil {
		t.Fatalf("SetErrorPayload failed: %s", err)
	}
	payload, err := m.GetErrorPayload()
	if err != nil {
		t.Fatalf("GetErrorPayload failed: %s", err)
	}
	if payload.ErrorCode != ErrorRoomNotFound {
		t.Fatalf("expected error code %d, got %d", ErrorRoomNotFound, payload.ErrorCode)
	}
	if payload.ErrorMessage != "room not found" {
		t.Fatalf("expected error message %q, got %q", "room not found", payload.ErrorMessage)
	}
}

func TestConnectPayloadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	if err := m.SetPayloadConnect(&TransporterMessagePayloadConnect{ProtocolVersion: ProtocolVersion}); err != nil {
		t.Fatalf("SetPayloadConnect failed: %s", err)
	}
	payload, err := m.GetPayloadConnect()
	if err != nil {
		t.Fatalf("GetPayloadConnect failed: %s", err)
	}
	if payload.ProtocolVersion != ProtocolVersion {
		t.Fatalf("expected protocol version %d, got %d", ProtocolVersion, payload.ProtocolVersion)
	}
}

func TestConnectResponsePayloadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	if err := m.SetPayloadConnectResponse(&TransporterMessagePayloadConnectResponse{ClientId: "ABCD1234"}); err != nil {
		t.Fatalf("SetPayloadConnectResponse failed: %s", err)
	}
	payload, err := m.GetPayloadConnectResponse()
	if err != nil {
		t.Fatalf("GetPayloadConnectResponse failed: %s", err)
	}
	if payload.ClientId != "ABCD1234" {
		t.Fatalf("expected client id %q, got %q", "ABCD1234", payload.ClientId)
	}
}

func TestCreateRoomResponsePayloadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	if err := m.SetPayloadCreateRoomResponse(&TransporterMessagePayloadCreateRoomResponse{RoomId: "ROOM1"}); err != nil {
		t.Fatalf("SetPayloadCreateRoomResponse failed: %s", err)
	}
	payload, err := m.GetPayloadCreateRoomResponse()
	if err != nil {
		t.Fatalf("GetPayloadCreateRoomResponse failed: %s", err)
	}
	if payload.RoomId != "ROOM1" {
		t.Fatalf("expected room id %q, got %q", "ROOM1", payload.RoomId)
	}
}

// TestConnectRoomPayloadRoundTrip exercises two strings written back to
// back, which is what exposed the original offset-slicing bug in
// writeString/readString (a non-zero offset silently produced empty or
// out-of-range slices).
func TestConnectRoomPayloadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	publicKey := []byte{0x00, 0x01, 0xff, 0xfe, 0x7f} // arbitrary bytes, including non-UTF8, like a real ed25519 key
	if err := m.SetPayloadConnectRoom(&TransporterMessagePayloadConnectRoom{
		RoomId:    "ROOM-A-LONGER-ID",
		ClientId:  "CLIENT-B",
		PublicKey: publicKey,
	}); err != nil {
		t.Fatalf("SetPayloadConnectRoom failed: %s", err)
	}
	payload, err := m.GetPayloadConnectRoom()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}
	if payload.RoomId != "ROOM-A-LONGER-ID" {
		t.Fatalf("expected room id %q, got %q", "ROOM-A-LONGER-ID", payload.RoomId)
	}
	if payload.ClientId != "CLIENT-B" {
		t.Fatalf("expected client id %q, got %q", "CLIENT-B", payload.ClientId)
	}
	if !bytes.Equal(payload.PublicKey, publicKey) {
		t.Fatalf("expected public key %x, got %x", publicKey, payload.PublicKey)
	}
}

func TestConnectRoomPayloadWithoutPublicKey(t *testing.T) {
	m := CreateTransporterMessage()
	if err := m.SetPayloadConnectRoom(&TransporterMessagePayloadConnectRoom{RoomId: "ROOM1"}); err != nil {
		t.Fatalf("SetPayloadConnectRoom failed: %s", err)
	}
	payload, err := m.GetPayloadConnectRoom()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoom failed: %s", err)
	}
	if len(payload.PublicKey) != 0 {
		t.Fatalf("expected an empty public key, got %x", payload.PublicKey)
	}
}

func TestConnectRoomResultPayloadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	if err := m.SetPayloadConnectRoomResult(&TransporterMessagePayloadConnectRoomResult{Accepted: 1}); err != nil {
		t.Fatalf("SetPayloadConnectRoomResult failed: %s", err)
	}
	payload, err := m.GetPayloadConnectRoomResponse()
	if err != nil {
		t.Fatalf("GetPayloadConnectRoomResponse failed: %s", err)
	}
	if payload.Accepted != 1 {
		t.Fatalf("expected accepted=1, got %d", payload.Accepted)
	}
}

func TestReadIntRejectsTruncatedBuffer(t *testing.T) {
	m := CreateTransporterMessage()
	if err := m.SetRawPayload([]byte{1, 2}); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if _, _, err := m.readInt(0); err == nil {
		t.Fatalf("expected an error reading an int from a 2-byte payload")
	}
}

func TestReadStringRejectsTruncatedBuffer(t *testing.T) {
	m := CreateTransporterMessage()
	// Declares a 100 byte string but only backs it with the header.
	if err := m.SetRawPayload([]byte{100, 0, 0, 0}); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if _, _, err := m.readString(0); err == nil {
		t.Fatalf("expected an error reading a string whose declared length exceeds the buffer")
	}
}
