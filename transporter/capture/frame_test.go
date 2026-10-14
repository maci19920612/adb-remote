package capture

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
)

func TestBuildPacketIPv4(t *testing.T) {
	src := netip.MustParseAddrPort("192.0.2.1:50000")
	dst := netip.MustParseAddrPort("192.0.2.2:9000")
	payload := []byte("hello")

	packet := buildPacket(segment{src: src, dst: dst, seq: 7, ack: 9, flags: flagPSH | flagACK, payload: payload})

	if packet[0] != 0x45 {
		t.Fatalf("expected an IPv4 header without options (0x45), got %#x", packet[0])
	}
	if totalLength := binary.BigEndian.Uint16(packet[2:4]); int(totalLength) != len(packet) || len(packet) != 20+20+len(payload) {
		t.Fatalf("unexpected IPv4 total length %d for a %d byte packet", totalLength, len(packet))
	}
	if packet[9] != 6 {
		t.Fatalf("expected the TCP protocol number (6), got %d", packet[9])
	}
	if !bytes.Equal(packet[12:16], src.Addr().AsSlice()) || !bytes.Equal(packet[16:20], dst.Addr().AsSlice()) {
		t.Fatalf("unexpected IPv4 addresses %v -> %v", packet[12:16], packet[16:20])
	}
	if onesComplementSum(packet[:20]) != 0xFFFF {
		t.Fatalf("invalid IPv4 header checksum")
	}

	tcp := packet[20:]
	assertTCPHeader(t, tcp, src.Port(), dst.Port(), 7, 9, flagPSH|flagACK, payload)
	pseudoHeader := append(append(append([]byte{}, packet[12:20]...), 0, 6), byte(len(tcp)>>8), byte(len(tcp)))
	if onesComplementSum(append(pseudoHeader, tcp...)) != 0xFFFF {
		t.Fatalf("invalid TCP checksum")
	}
}

func TestBuildPacketIPv6(t *testing.T) {
	src := netip.MustParseAddrPort("[2001:db8::1]:50000")
	dst := netip.MustParseAddrPort("[2001:db8::2]:9000")
	payload := []byte("hello!")

	packet := buildPacket(segment{src: src, dst: dst, seq: 1, ack: 1, flags: flagSYN, payload: payload})

	if packet[0]>>4 != 6 {
		t.Fatalf("expected an IPv6 header, got version %d", packet[0]>>4)
	}
	if payloadLength := binary.BigEndian.Uint16(packet[4:6]); int(payloadLength) != 20+len(payload) {
		t.Fatalf("unexpected IPv6 payload length %d", payloadLength)
	}
	if packet[6] != 6 {
		t.Fatalf("expected the TCP next header (6), got %d", packet[6])
	}
	if !bytes.Equal(packet[8:24], src.Addr().AsSlice()) || !bytes.Equal(packet[24:40], dst.Addr().AsSlice()) {
		t.Fatalf("unexpected IPv6 addresses")
	}

	tcp := packet[40:]
	assertTCPHeader(t, tcp, src.Port(), dst.Port(), 1, 1, flagSYN, payload)
	pseudoHeader := append(append([]byte{}, packet[8:40]...), 0, 0, byte(len(tcp)>>8), byte(len(tcp)), 0, 0, 0, 6)
	if onesComplementSum(append(pseudoHeader, tcp...)) != 0xFFFF {
		t.Fatalf("invalid TCP checksum")
	}
}

func TestEndpointPairUnmapsIPv4MappedAddresses(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("::ffff:192.0.2.1"), Port: 50000}
	local := &net.TCPAddr{IP: net.ParseIP("192.0.2.2"), Port: 9000}

	client, server := endpointPair(remote, local)

	if client != netip.MustParseAddrPort("192.0.2.1:50000") || server != netip.MustParseAddrPort("192.0.2.2:9000") {
		t.Fatalf("expected plain IPv4 endpoints, got %s -> %s", client, server)
	}
}

func TestEndpointPairMapsMixedFamiliesToIPv6(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 50000}
	local := &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 9000}

	client, server := endpointPair(remote, local)

	if !client.Addr().Is6() || !server.Addr().Is6() {
		t.Fatalf("expected both endpoints to be IPv6 so they fit one packet, got %s -> %s", client, server)
	}
	if client.Addr().Unmap() != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("expected the IPv4 client address to be preserved as IPv4-mapped, got %s", client.Addr())
	}
}

func TestEndpointPairFallsBackForNonTCPAddresses(t *testing.T) {
	client, server := endpointPair(&net.UnixAddr{Name: "a", Net: "unix"}, &net.UnixAddr{Name: "b", Net: "unix"})

	if !client.Addr().Is4() || !server.Addr().Is4() {
		t.Fatalf("expected IPv4 placeholder endpoints, got %s -> %s", client, server)
	}
}

func assertTCPHeader(t *testing.T, tcp []byte, srcPort, dstPort uint16, seq, ack uint32, flags uint8, payload []byte) {
	t.Helper()
	if got := binary.BigEndian.Uint16(tcp[0:2]); got != srcPort {
		t.Fatalf("expected source port %d, got %d", srcPort, got)
	}
	if got := binary.BigEndian.Uint16(tcp[2:4]); got != dstPort {
		t.Fatalf("expected destination port %d, got %d", dstPort, got)
	}
	if got := binary.BigEndian.Uint32(tcp[4:8]); got != seq {
		t.Fatalf("expected seq %d, got %d", seq, got)
	}
	if got := binary.BigEndian.Uint32(tcp[8:12]); got != ack {
		t.Fatalf("expected ack %d, got %d", ack, got)
	}
	if dataOffset := tcp[12] >> 4; dataOffset != 5 {
		t.Fatalf("expected a 5 word TCP header, got %d", dataOffset)
	}
	if tcp[13] != flags {
		t.Fatalf("expected flags %#x, got %#x", flags, tcp[13])
	}
	if !bytes.Equal(tcp[20:], payload) {
		t.Fatalf("expected payload %q, got %q", payload, tcp[20:])
	}
}

// onesComplementSum is an independent implementation of the Internet
// checksum sum; a correctly checksummed header (pseudo-header included for
// TCP) sums to 0xFFFF.
func onesComplementSum(data []byte) uint16 {
	var sum uint32
	for i := 0; i < len(data); i += 2 {
		word := uint32(data[i]) << 8
		if i+1 < len(data) {
			word |= uint32(data[i+1])
		}
		sum += word
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return uint16(sum)
}
