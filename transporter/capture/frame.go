package capture

import (
	"encoding/binary"
	"net"
	"net/netip"
)

// TCP header flags.
const (
	flagFIN uint8 = 0x01
	flagSYN uint8 = 0x02
	flagPSH uint8 = 0x08
	flagACK uint8 = 0x10
)

const (
	ipProtoTCP     = 6
	ipv4HeaderSize = 20
	ipv6HeaderSize = 40
	tcpHeaderSize  = 20

	// maxSegmentPayload keeps every synthetic packet within the 16-bit
	// IPv4 total length / IPv6 payload length fields; larger reads and
	// writes are recorded as several consecutive segments.
	maxSegmentPayload = 0xFFFF - ipv6HeaderSize - tcpHeaderSize
)

// segment is one synthetic TCP segment: the bytes that actually crossed
// the socket, framed with the real endpoints and sequence numbers so
// Wireshark can reassemble the stream and dissect the TLS inside it.
type segment struct {
	src, dst netip.AddrPort
	seq, ack uint32
	flags    uint8
	payload  []byte
}

// buildPacket frames s as a raw IPv4 or IPv6 packet (LINKTYPE_RAW),
// depending on the address family of its endpoints, which endpointPair
// guarantees to match.
func buildPacket(s segment) []byte {
	tcpLength := tcpHeaderSize + len(s.payload)
	src, dst := s.src.Addr().AsSlice(), s.dst.Addr().AsSlice()

	var packet, pseudoHeader []byte
	if s.src.Addr().Is4() {
		packet = make([]byte, ipv4HeaderSize, ipv4HeaderSize+tcpLength)
		packet[0] = 0x45 // version 4, IHL 5 (20 bytes, no options)
		binary.BigEndian.PutUint16(packet[2:4], uint16(ipv4HeaderSize+tcpLength))
		binary.BigEndian.PutUint16(packet[6:8], 0x4000) // don't fragment
		packet[8] = 64                                  // TTL
		packet[9] = ipProtoTCP
		copy(packet[12:16], src)
		copy(packet[16:20], dst)
		binary.BigEndian.PutUint16(packet[10:12], checksum(packet))

		pseudoHeader = append(append([]byte{}, packet[12:20]...), 0, ipProtoTCP)
		pseudoHeader = binary.BigEndian.AppendUint16(pseudoHeader, uint16(tcpLength))
	} else {
		packet = make([]byte, ipv6HeaderSize, ipv6HeaderSize+tcpLength)
		packet[0] = 0x60 // version 6, traffic class and flow label 0
		binary.BigEndian.PutUint16(packet[4:6], uint16(tcpLength))
		packet[6] = ipProtoTCP
		packet[7] = 64 // hop limit
		copy(packet[8:24], src)
		copy(packet[24:40], dst)

		pseudoHeader = append([]byte{}, packet[8:40]...)
		pseudoHeader = binary.BigEndian.AppendUint32(pseudoHeader, uint32(tcpLength))
		pseudoHeader = append(pseudoHeader, 0, 0, 0, ipProtoTCP)
	}

	tcp := make([]byte, tcpHeaderSize, tcpLength)
	binary.BigEndian.PutUint16(tcp[0:2], s.src.Port())
	binary.BigEndian.PutUint16(tcp[2:4], s.dst.Port())
	binary.BigEndian.PutUint32(tcp[4:8], s.seq)
	binary.BigEndian.PutUint32(tcp[8:12], s.ack)
	tcp[12] = (tcpHeaderSize / 4) << 4 // data offset, no options
	tcp[13] = s.flags
	binary.BigEndian.PutUint16(tcp[14:16], 0xFFFF) // window
	tcp = append(tcp, s.payload...)
	binary.BigEndian.PutUint16(tcp[16:18], checksum(append(pseudoHeader, tcp...)))

	return append(packet, tcp...)
}

// checksum computes the Internet checksum (RFC 1071): the ones-complement
// of the ones-complement sum of data's 16-bit words, with the checksum
// field itself taken as 0.
func checksum(data []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

// endpointPair resolves a connection's client (remote) and server (local)
// addresses into endpoints of the same IP family, so they fit one packet
// header: IPv4-mapped addresses are unmapped, and a genuinely mixed pair is
// promoted to IPv6. Non-TCP addresses fall back to 0.0.0.0:0.
func endpointPair(remote net.Addr, local net.Addr) (client netip.AddrPort, server netip.AddrPort) {
	client, server = tcpEndpoint(remote), tcpEndpoint(local)
	if client.Addr().Is4() != server.Addr().Is4() {
		client = netip.AddrPortFrom(netip.AddrFrom16(client.Addr().As16()), client.Port())
		server = netip.AddrPortFrom(netip.AddrFrom16(server.Addr().As16()), server.Port())
	}
	return client, server
}

func tcpEndpoint(addr net.Addr) netip.AddrPort {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.AddrPortFrom(netip.IPv4Unspecified(), 0)
	}
	addrPort := tcpAddr.AddrPort()
	if !addrPort.Addr().IsValid() {
		return netip.AddrPortFrom(netip.IPv4Unspecified(), addrPort.Port())
	}
	return netip.AddrPortFrom(addrPort.Addr().Unmap(), addrPort.Port())
}
