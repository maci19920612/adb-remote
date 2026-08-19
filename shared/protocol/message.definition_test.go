package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestSetDirectResponseErrorCommand(t *testing.T) {
	m := CreateTransporterMessage()
	m.SetDirectCommand(CommandCreateRoom)
	if m.Command() != CommandCreateRoom {
		t.Fatalf("expected command %x, got %x", CommandCreateRoom, m.Command())
	}

	m.SetResponseCommand(CommandCreateRoom)
	if m.Command() != CommandCreateRoom|CommandResponseMask {
		t.Fatalf("expected response command %x, got %x", CommandCreateRoom|CommandResponseMask, m.Command())
	}
	if m.IsError() {
		t.Fatalf("response command should not be flagged as an error")
	}

	m.SetErrorResponseCommand(CommandCreateRoom)
	if m.Command() != CommandCreateRoom|CommandErrorResponseMask {
		t.Fatalf("expected error response command %x, got %x", CommandCreateRoom|CommandErrorResponseMask, m.Command())
	}
	if !m.IsError() {
		t.Fatalf("expected IsError() to be true for an error response command")
	}
}

func TestSetRawPayloadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	data := []byte("hello world")
	if err := m.SetRawPayload(data); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}
	if m.PayloadLength() != uint32(len(data)) {
		t.Fatalf("expected payload length %d, got %d", len(data), m.PayloadLength())
	}
	if !bytes.Equal(m.Payload(), data) {
		t.Fatalf("expected payload %q, got %q", data, m.Payload())
	}
}

func TestSetRawPayloadTooLarge(t *testing.T) {
	m := CreateTransporterMessage()
	oversized := make([]byte, MaxPayloadSize+1)
	if err := m.SetRawPayload(oversized); err == nil {
		t.Fatalf("expected an error when the payload exceeds MaxPayloadSize")
	}
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	m := CreateTransporterMessage()
	m.SetDirectCommand(CommandJoinRoom)
	if err := m.SetRawPayload([]byte("payload-data")); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}

	buffer := &bytes.Buffer{}
	if err := m.Write(buffer); err != nil {
		t.Fatalf("Write failed: %s", err)
	}

	received := CreateTransporterMessage()
	if err := received.Read(buffer); err != nil {
		t.Fatalf("Read failed: %s", err)
	}
	if received.Command() != CommandJoinRoom {
		t.Fatalf("expected command %x, got %x", CommandJoinRoom, received.Command())
	}
	if !bytes.Equal(received.Payload(), []byte("payload-data")) {
		t.Fatalf("expected payload %q, got %q", "payload-data", received.Payload())
	}
}

func TestReadRejectsCorruptedPayload(t *testing.T) {
	m := CreateTransporterMessage()
	m.SetDirectCommand(CommandJoinRoom)
	if err := m.SetRawPayload([]byte("payload-data")); err != nil {
		t.Fatalf("SetRawPayload failed: %s", err)
	}

	buffer := &bytes.Buffer{}
	if err := m.Write(buffer); err != nil {
		t.Fatalf("Write failed: %s", err)
	}
	corrupted := buffer.Bytes()
	// Flip a byte inside the payload region (after the 12 byte header).
	corrupted[12] ^= 0xFF

	received := CreateTransporterMessage()
	err := received.Read(bytes.NewReader(corrupted))
	if err == nil {
		t.Fatalf("expected a crc32 validation error for a corrupted payload")
	}
}

func TestReadRejectsOversizedPayloadLength(t *testing.T) {
	header := make([]byte, 12)
	ByteOrder.PutUint32(header[0:4], CommandJoinRoom)
	ByteOrder.PutUint32(header[4:8], MaxPayloadSize+1)
	ByteOrder.PutUint32(header[8:12], 0)

	m := CreateTransporterMessage()
	err := m.Read(bytes.NewReader(header))
	if err != ErrPayloadTooLarge {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}
}

func TestReadPropagatesShortReadError(t *testing.T) {
	m := CreateTransporterMessage()
	err := m.Read(bytes.NewReader([]byte{1, 2, 3}))
	if err == nil || err == io.EOF {
		t.Fatalf("expected a short-read error, got %v", err)
	}
}

func TestReadNoPayload(t *testing.T) {
	m := CreateTransporterMessage()
	m.SetDirectCommand(CommandCreateRoom)

	buffer := &bytes.Buffer{}
	if err := m.Write(buffer); err != nil {
		t.Fatalf("Write failed: %s", err)
	}

	received := CreateTransporterMessage()
	if err := received.Read(buffer); err != nil {
		t.Fatalf("Read failed: %s", err)
	}
	if received.PayloadLength() != 0 {
		t.Fatalf("expected an empty payload, got length %d", received.PayloadLength())
	}
}
