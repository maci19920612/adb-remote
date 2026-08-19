package adb

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestSetAndReadBackFields(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandConnect, 1, MaxPayloadLength, []byte("hello")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if m.Command() != CommandConnect {
		t.Fatalf("expected command %x, got %x", CommandConnect, m.Command())
	}
	if m.Arg1() != 1 {
		t.Fatalf("expected arg1 1, got %d", m.Arg1())
	}
	if m.Arg2() != MaxPayloadLength {
		t.Fatalf("expected arg2 %d, got %d", MaxPayloadLength, m.Arg2())
	}
	if m.DataString() != "hello" {
		t.Fatalf("expected data %q, got %q", "hello", m.DataString())
	}
	if m.Magic() != CommandConnect^magicConstant {
		t.Fatalf("expected magic %x, got %x", CommandConnect^magicConstant, m.Magic())
	}
}

func TestSetRejectsOversizedData(t *testing.T) {
	m := CreateMessage()
	oversized := make([]byte, MaxPayloadLength+1)
	if err := m.Set(CommandWrite, 0, 0, oversized); err == nil {
		t.Fatalf("expected an error for data exceeding MaxPayloadLength")
	}
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandWrite, 7, 9, []byte("payload")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}

	buffer := &bytes.Buffer{}
	if err := m.Write(buffer); err != nil {
		t.Fatalf("Write failed: %s", err)
	}

	received := CreateMessage()
	if err := received.Read(buffer); err != nil {
		t.Fatalf("Read failed: %s", err)
	}
	if received.Command() != CommandWrite {
		t.Fatalf("expected command %x, got %x", CommandWrite, received.Command())
	}
	if received.DataString() != "payload" {
		t.Fatalf("expected data %q, got %q", "payload", received.DataString())
	}
}

func TestReadRejectsInvalidCommand(t *testing.T) {
	buffer := &bytes.Buffer{}
	header := make([]byte, HeaderSize)
	// An arbitrary command that is not in the supported set.
	binary.LittleEndian.PutUint32(header[0:4], 0xdeadbeef)
	buffer.Write(header)

	received := CreateMessage()
	if err := received.Read(buffer); err == nil {
		t.Fatalf("expected an error for an unsupported command")
	}
}

func TestReadRejectsBadMagic(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandOkay, 0, 0, nil); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	raw := append([]byte{}, m.Bytes()...)
	// Corrupt the magic field (last 4 bytes of the header).
	raw[20] ^= 0xFF

	received := CreateMessage()
	if err := received.Read(bytes.NewReader(raw)); err == nil {
		t.Fatalf("expected an error for a mismatched magic value")
	}
}

func TestReadRejectsCorruptedData(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandWrite, 0, 0, []byte("payload")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	raw := append([]byte{}, m.Bytes()...)
	raw[HeaderSize] ^= 0xFF // flip a byte inside the data region

	received := CreateMessage()
	if err := received.Read(bytes.NewReader(raw)); err == nil {
		t.Fatalf("expected a crc32 mismatch error for corrupted data")
	}
}

func TestDecodeMessageRoundTrip(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandOkay, 3, 4, []byte("abc")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	decoded, err := DecodeMessage(m.Bytes())
	if err != nil {
		t.Fatalf("DecodeMessage failed: %s", err)
	}
	if decoded.Command() != CommandOkay {
		t.Fatalf("expected command %x, got %x", CommandOkay, decoded.Command())
	}
	if decoded.DataString() != "abc" {
		t.Fatalf("expected data %q, got %q", "abc", decoded.DataString())
	}
}

func TestDecodeMessageRejectsTooShortBuffer(t *testing.T) {
	if _, err := DecodeMessage(make([]byte, HeaderSize-1)); err != ErrMessageTooShort {
		t.Fatalf("expected ErrMessageTooShort, got %v", err)
	}
}

func TestDecodeMessageRejectsUnderfilledDataLength(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandOkay, 0, 0, []byte("abcdef")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	// Truncate the buffer so the declared data length no longer fits.
	truncated := m.Bytes()[:HeaderSize+2]
	if _, err := DecodeMessage(truncated); err == nil {
		t.Fatalf("expected an error when the declared data length exceeds the buffer")
	}
}

func TestBytesReflectsCurrentDataLength(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandWrite, 0, 0, []byte("1234567890")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	if len(m.Bytes()) != HeaderSize+10 {
		t.Fatalf("expected Bytes() length %d, got %d", HeaderSize+10, len(m.Bytes()))
	}
}
