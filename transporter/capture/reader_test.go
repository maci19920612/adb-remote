package capture

import (
	"encoding/binary"
	"net/netip"
	"os"
	"testing"
)

// capturedSegment is a TCP segment decoded back out of a capture file.
type capturedSegment struct {
	src, dst netip.AddrPort
	seq, ack uint32
	flags    uint8
	payload  []byte
}

// capturedFile is a capture file decoded into the parts the tests assert
// on: the block types in file order, every TCP segment and every TLS key
// log secret.
type capturedFile struct {
	blockTypes []uint32
	segments   []capturedSegment
	secrets    [][]byte
}

func readCaptureFile(t *testing.T, path string) capturedFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read the capture file: %s", err)
	}

	var result capturedFile
	for offset := 0; offset < len(data); {
		if len(data)-offset < 12 {
			t.Fatalf("truncated block at offset %d", offset)
		}
		blockType := binary.LittleEndian.Uint32(data[offset : offset+4])
		length := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if length < 12 || offset+length > len(data) {
			t.Fatalf("invalid block length %d at offset %d", length, offset)
		}
		body := data[offset+8 : offset+length-4]
		result.blockTypes = append(result.blockTypes, blockType)

		switch blockType {
		case blockTypeEnhancedPacket:
			capturedLength := binary.LittleEndian.Uint32(body[12:16])
			result.segments = append(result.segments, parseSegment(t, body[20:20+capturedLength]))
		case blockTypeDecryptionSecrets:
			secretsLength := binary.LittleEndian.Uint32(body[4:8])
			result.secrets = append(result.secrets, body[8:8+secretsLength])
		}
		offset += length
	}
	return result
}

func parseSegment(t *testing.T, packet []byte) capturedSegment {
	t.Helper()
	var src, dst netip.Addr
	var tcp []byte
	switch packet[0] >> 4 {
	case 4:
		src = netip.AddrFrom4([4]byte(packet[12:16]))
		dst = netip.AddrFrom4([4]byte(packet[16:20]))
		tcp = packet[20:]
	case 6:
		src = netip.AddrFrom16([16]byte(packet[8:24]))
		dst = netip.AddrFrom16([16]byte(packet[24:40]))
		tcp = packet[40:]
	default:
		t.Fatalf("unexpected IP version %d", packet[0]>>4)
	}
	return capturedSegment{
		src:     netip.AddrPortFrom(src, binary.BigEndian.Uint16(tcp[0:2])),
		dst:     netip.AddrPortFrom(dst, binary.BigEndian.Uint16(tcp[2:4])),
		seq:     binary.BigEndian.Uint32(tcp[4:8]),
		ack:     binary.BigEndian.Uint32(tcp[8:12]),
		flags:   tcp[13],
		payload: tcp[20:],
	}
}
