package pcapwriter

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"time"
)

func TestNewWriterWritesValidGlobalHeader(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewWriter(&buf); err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}
	header := buf.Bytes()
	if len(header) != 24 {
		t.Fatalf("expected a 24 byte global header, got %d", len(header))
	}
	if magic := binary.LittleEndian.Uint32(header[0:4]); magic != magicMicroseconds {
		t.Fatalf("expected magic number %x, got %x", magicMicroseconds, magic)
	}
	if major := binary.LittleEndian.Uint16(header[4:6]); major != versionMajor {
		t.Fatalf("expected version major %d, got %d", versionMajor, major)
	}
	if minor := binary.LittleEndian.Uint16(header[6:8]); minor != versionMinor {
		t.Fatalf("expected version minor %d, got %d", versionMinor, minor)
	}
	if linkType := binary.LittleEndian.Uint32(header[20:24]); linkType != linkTypeEthernet {
		t.Fatalf("expected link type %d, got %d", linkTypeEthernet, linkType)
	}
}

// parsedPacket is a minimal, from-scratch decode of one captured frame,
// independent of the encoder's own helper functions (aside from the
// checksum self-check, which is inherently symmetric), used to verify
// WritePacket actually produced a well-formed Ethernet/IPv4/UDP frame.
type parsedPacket struct {
	tsSec, tsUsec    uint32
	inclLen, origLen uint32
	srcMAC, dstMAC   [6]byte
	etherType        uint16
	srcIP, dstIP     [4]byte
	ipChecksumValid  bool
	srcPort, dstPort uint16
	udpLength        uint16
	payload          []byte
}

func parseOnePacket(t *testing.T, r *bytes.Reader) parsedPacket {
	t.Helper()
	record := make([]byte, 16)
	if _, err := io.ReadFull(r, record); err != nil {
		t.Fatalf("failed to read the packet record header: %s", err)
	}
	var p parsedPacket
	p.tsSec = binary.LittleEndian.Uint32(record[0:4])
	p.tsUsec = binary.LittleEndian.Uint32(record[4:8])
	p.inclLen = binary.LittleEndian.Uint32(record[8:12])
	p.origLen = binary.LittleEndian.Uint32(record[12:16])

	frame := make([]byte, p.inclLen)
	if _, err := io.ReadFull(r, frame); err != nil {
		t.Fatalf("failed to read the frame: %s", err)
	}

	copy(p.dstMAC[:], frame[0:6])
	copy(p.srcMAC[:], frame[6:12])
	p.etherType = binary.BigEndian.Uint16(frame[12:14])

	ip := frame[14:]
	if version := ip[0] >> 4; version != 4 {
		t.Fatalf("expected IPv4, got version %d", version)
	}
	if ihl := ip[0] & 0x0F; ihl != 5 {
		t.Fatalf("expected a 20-byte IPv4 header (IHL=5), got IHL=%d", ihl)
	}
	if proto := ip[9]; proto != ipProtoUDP {
		t.Fatalf("expected IP protocol UDP (%d), got %d", ipProtoUDP, proto)
	}
	copy(p.srcIP[:], ip[12:16])
	copy(p.dstIP[:], ip[16:20])
	// A correctly checksummed header sums (as-is, checksum field included)
	// to all-ones, i.e. ipv4Checksum of the whole thing is 0.
	p.ipChecksumValid = ipv4Checksum(ip[:20]) == 0

	udp := ip[20:]
	p.srcPort = binary.BigEndian.Uint16(udp[0:2])
	p.dstPort = binary.BigEndian.Uint16(udp[2:4])
	p.udpLength = binary.BigEndian.Uint16(udp[4:6])
	p.payload = udp[8:]

	return p
}

func TestWritePacketOutgoingFrameIsWellFormed(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}
	payload := []byte("hello transporter")
	ts := time.Unix(1700000000, 123456000)
	if err := w.WritePacket(Outgoing, payload, ts); err != nil {
		t.Fatalf("WritePacket failed: %s", err)
	}

	r := bytes.NewReader(buf.Bytes()[24:]) // skip the global header
	p := parseOnePacket(t, r)

	if p.tsSec != 1700000000 {
		t.Fatalf("expected ts_sec 1700000000, got %d", p.tsSec)
	}
	if p.tsUsec != 123456 {
		t.Fatalf("expected ts_usec 123456, got %d", p.tsUsec)
	}
	if p.etherType != etherTypeIPv4 {
		t.Fatalf("expected ethertype IPv4 (%x), got %x", etherTypeIPv4, p.etherType)
	}
	if p.srcMAC != macClient || p.dstMAC != macTransporter {
		t.Fatalf("expected client->transporter MACs, got src=%v dst=%v", p.srcMAC, p.dstMAC)
	}
	if p.srcIP != ipClient || p.dstIP != ipTransporter {
		t.Fatalf("expected client->transporter IPs, got src=%v dst=%v", p.srcIP, p.dstIP)
	}
	if !p.ipChecksumValid {
		t.Fatalf("expected a valid IPv4 header checksum")
	}
	if p.srcPort != portClient || p.dstPort != portTransporter {
		t.Fatalf("expected client->transporter ports, got src=%d dst=%d", p.srcPort, p.dstPort)
	}
	if int(p.udpLength) != 8+len(payload) {
		t.Fatalf("expected UDP length %d, got %d", 8+len(payload), p.udpLength)
	}
	if !bytes.Equal(p.payload, payload) {
		t.Fatalf("expected payload %q, got %q", payload, p.payload)
	}
}

func TestWritePacketIncomingReversesDirection(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}
	if err := w.WritePacket(Incoming, []byte("reply"), time.Now()); err != nil {
		t.Fatalf("WritePacket failed: %s", err)
	}

	r := bytes.NewReader(buf.Bytes()[24:])
	p := parseOnePacket(t, r)

	if p.srcMAC != macTransporter || p.dstMAC != macClient {
		t.Fatalf("expected transporter->client MACs, got src=%v dst=%v", p.srcMAC, p.dstMAC)
	}
	if p.srcIP != ipTransporter || p.dstIP != ipClient {
		t.Fatalf("expected transporter->client IPs, got src=%v dst=%v", p.srcIP, p.dstIP)
	}
	if p.srcPort != portTransporter || p.dstPort != portClient {
		t.Fatalf("expected transporter->client ports, got src=%d dst=%d", p.srcPort, p.dstPort)
	}
	if !p.ipChecksumValid {
		t.Fatalf("expected a valid IPv4 header checksum")
	}
}

func TestWritePacketMultiplePacketsAreSequential(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}
	if err := w.WritePacket(Outgoing, []byte("one"), time.Now()); err != nil {
		t.Fatalf("WritePacket failed: %s", err)
	}
	if err := w.WritePacket(Incoming, []byte("two"), time.Now()); err != nil {
		t.Fatalf("WritePacket failed: %s", err)
	}

	r := bytes.NewReader(buf.Bytes()[24:])
	first := parseOnePacket(t, r)
	second := parseOnePacket(t, r)

	if !bytes.Equal(first.payload, []byte("one")) {
		t.Fatalf("expected the first packet's payload to be %q, got %q", "one", first.payload)
	}
	if !bytes.Equal(second.payload, []byte("two")) {
		t.Fatalf("expected the second packet's payload to be %q, got %q", "two", second.payload)
	}
	if r.Len() != 0 {
		t.Fatalf("expected exactly two packets, %d trailing bytes remain", r.Len())
	}
}

func TestWritePacketLargePayload(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter failed: %s", err)
	}
	// Matches shared/protocol.MaxPayloadSize + HeaderSize, the largest a
	// real captured message can be.
	payload := bytes.Repeat([]byte{0xAB}, 0xF000+12)
	if err := w.WritePacket(Outgoing, payload, time.Now()); err != nil {
		t.Fatalf("WritePacket failed: %s", err)
	}

	r := bytes.NewReader(buf.Bytes()[24:])
	p := parseOnePacket(t, r)
	if !bytes.Equal(p.payload, payload) {
		t.Fatalf("expected the large payload to round-trip unchanged (len %d), got len %d", len(payload), len(p.payload))
	}
}
