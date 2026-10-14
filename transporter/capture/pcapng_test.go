package capture

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestSectionHeaderBlockLayout(t *testing.T) {
	block := sectionHeaderBlock()

	if len(block) != 28 {
		t.Fatalf("expected a 28 byte section header block, got %d", len(block))
	}
	if blockType := binary.LittleEndian.Uint32(block[0:4]); blockType != 0x0A0D0D0A {
		t.Fatalf("unexpected block type %#x", blockType)
	}
	if magic := binary.LittleEndian.Uint32(block[8:12]); magic != 0x1A2B3C4D {
		t.Fatalf("unexpected byte order magic %#x", magic)
	}
	if major, minor := binary.LittleEndian.Uint16(block[12:14]), binary.LittleEndian.Uint16(block[14:16]); major != 1 || minor != 0 {
		t.Fatalf("unexpected version %d.%d", major, minor)
	}
	if sectionLength := int64(binary.LittleEndian.Uint64(block[16:24])); sectionLength != -1 {
		t.Fatalf("expected an unspecified (-1) section length, got %d", sectionLength)
	}
	assertBlockLengths(t, block)
}

func TestInterfaceDescriptionBlockUsesRawIPLinkType(t *testing.T) {
	block := interfaceDescriptionBlock()

	if blockType := binary.LittleEndian.Uint32(block[0:4]); blockType != 1 {
		t.Fatalf("unexpected block type %#x", blockType)
	}
	if linkType := binary.LittleEndian.Uint16(block[8:10]); linkType != 101 {
		t.Fatalf("expected LINKTYPE_RAW (101), got %d", linkType)
	}
	if snapLen := binary.LittleEndian.Uint32(block[12:16]); snapLen != 0 {
		t.Fatalf("expected an unlimited (0) snap length, got %d", snapLen)
	}
	assertBlockLengths(t, block)
}

func TestEnhancedPacketBlockCarriesTimestampAndPaddedPacket(t *testing.T) {
	ts := time.Unix(1_700_000_000, 123_456_000)
	packet := []byte{1, 2, 3, 4, 5}

	block := enhancedPacketBlock(ts, packet)

	if blockType := binary.LittleEndian.Uint32(block[0:4]); blockType != 6 {
		t.Fatalf("unexpected block type %#x", blockType)
	}
	if len(block) != 32+8 {
		t.Fatalf("expected the 5 byte packet to be padded to 8 bytes (block length 40), got %d", len(block))
	}
	if interfaceID := binary.LittleEndian.Uint32(block[8:12]); interfaceID != 0 {
		t.Fatalf("expected interface 0, got %d", interfaceID)
	}
	micros := uint64(binary.LittleEndian.Uint32(block[12:16]))<<32 | uint64(binary.LittleEndian.Uint32(block[16:20]))
	if micros != uint64(ts.UnixMicro()) {
		t.Fatalf("expected timestamp %d µs, got %d", ts.UnixMicro(), micros)
	}
	if captured, original := binary.LittleEndian.Uint32(block[20:24]), binary.LittleEndian.Uint32(block[24:28]); captured != 5 || original != 5 {
		t.Fatalf("expected captured/original length 5/5, got %d/%d", captured, original)
	}
	if !bytes.Equal(block[28:33], packet) || !bytes.Equal(block[33:36], []byte{0, 0, 0}) {
		t.Fatalf("expected the packet followed by zero padding, got %v", block[28:36])
	}
	assertBlockLengths(t, block)
}

func TestDecryptionSecretsBlockCarriesTLSKeyLog(t *testing.T) {
	secrets := []byte("CLIENT_RANDOM 00 11\n")

	block := decryptionSecretsBlock(secrets)

	if blockType := binary.LittleEndian.Uint32(block[0:4]); blockType != 0x0A {
		t.Fatalf("unexpected block type %#x", blockType)
	}
	if secretsType := binary.LittleEndian.Uint32(block[8:12]); secretsType != 0x544c534b {
		t.Fatalf("expected the TLS key log secrets type, got %#x", secretsType)
	}
	if length := binary.LittleEndian.Uint32(block[12:16]); length != uint32(len(secrets)) {
		t.Fatalf("expected secrets length %d, got %d", len(secrets), length)
	}
	if !bytes.Equal(block[16:16+len(secrets)], secrets) {
		t.Fatalf("expected the secrets to follow the header")
	}
	assertBlockLengths(t, block)
}

// assertBlockLengths checks the invariants every pcapng block shares: its
// length is a multiple of 4 and is repeated at both ends of the block.
func assertBlockLengths(t *testing.T, block []byte) {
	t.Helper()
	if len(block)%4 != 0 {
		t.Fatalf("block length %d is not a multiple of 4", len(block))
	}
	leading := binary.LittleEndian.Uint32(block[4:8])
	trailing := binary.LittleEndian.Uint32(block[len(block)-4:])
	if leading != uint32(len(block)) || trailing != uint32(len(block)) {
		t.Fatalf("expected leading/trailing block length %d, got %d/%d", len(block), leading, trailing)
	}
}
