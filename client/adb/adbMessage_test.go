package adb

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// realAdbCnxnHex is a genuine CNXN packet captured from a real, current-day
// `adb connect` (platform-tools 35.0.0). It is here as a regression test:
// an earlier version of this package validated the data_check field as a
// real CRC32, which every real adb client fails, since the actual wire
// protocol uses the sum of the payload bytes truncated to 32 bits.
const realAdbCnxnHex = "434e584e01000001000010000501000047660000bcb1a7b1686f73743a3a66656174757265733d7368656c6c5f76322c636d642c737461745f76322c6c735f76322c66697865645f707573685f6d6b6469722c617065782c6162622c66697865645f707573685f73796d6c696e6b5f74696d657374616d702c6162625f657865632c72656d6f756e745f7368656c6c2c747261636b5f6170702c73656e64726563765f76322c73656e64726563765f76325f62726f746c692c73656e64726563765f76325f6c7a342c73656e64726563765f76325f7a7374642c73656e64726563765f76325f6472795f72756e5f73656e642c6f70656e73637265656e5f6d646e732c646576696365747261636b65725f70726f746f5f666f726d6174"

func TestRealAdbCnxnPacketValidates(t *testing.T) {
	raw, err := hex.DecodeString(realAdbCnxnHex)
	if err != nil {
		t.Fatalf("bad test fixture: %s", err)
	}

	m := CreateMessage()
	if err := m.Read(bytes.NewReader(raw)); err != nil {
		t.Fatalf("a real adb CNXN packet must be accepted, got: %s", err)
	}
	if m.Command() != CommandConnect {
		t.Fatalf("expected command %x, got %x", CommandConnect, m.Command())
	}
	const realAdbProtocolVersionSkipChecksum = 0x01000001
	if m.Arg1() != realAdbProtocolVersionSkipChecksum {
		t.Fatalf("expected arg1 %x, got %x", realAdbProtocolVersionSkipChecksum, m.Arg1())
	}
	if m.Arg2() != 0x00100000 {
		t.Fatalf("expected arg2 (peer maxdata) %x, got %x", 0x00100000, m.Arg2())
	}
	if got := m.DataString()[:len("host::features=")]; got != "host::features=" {
		t.Fatalf("expected the data to start with %q, got %q", "host::features=", got)
	}
}

func TestChecksumIsAdditiveByteSumNotCrc32(t *testing.T) {
	data := []byte("host::features=shell_v2")
	var want uint32
	for _, b := range data {
		want += uint32(b)
	}
	if got := checksum(data); got != want {
		t.Fatalf("expected the additive byte-sum checksum %x, got %x", want, got)
	}
}

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

// TestReadDoesNotValidateDataChecksum documents a deliberate compatibility
// choice: real adb clients only compute a real data_check for the initial
// CNXN and send a literal 0 afterward (see checksum's doc comment), and do
// not validate what we send them either. Rejecting on a checksum mismatch
// would make this proxy unusable against every current adb build, so Read
// must accept a message whose declared checksum does not match its data.
func TestReadDoesNotValidateDataChecksum(t *testing.T) {
	m := CreateMessage()
	if err := m.Set(CommandWrite, 0, 0, []byte("payload")); err != nil {
		t.Fatalf("Set failed: %s", err)
	}
	raw := append([]byte{}, m.Bytes()...)
	raw[HeaderSize] ^= 0xFF // flip a byte inside the data region, checksum now stale

	received := CreateMessage()
	if err := received.Read(bytes.NewReader(raw)); err != nil {
		t.Fatalf("Read should not fail on a stale/mismatched checksum, got: %s", err)
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
