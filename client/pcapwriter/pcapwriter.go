// Package pcapwriter writes a classic (non-pcapng) .pcap file that wraps
// already-decrypted application-layer messages in synthetic
// Ethernet/IPv4/UDP frames, so a capture can be opened directly in
// Wireshark for debugging.
//
// This is not a real packet capture: adb-remote talks to the transporter
// over TLS, so the actual bytes on the wire are encrypted and useless to
// inspect without the session keys. What's actually useful for debugging
// is the plaintext protocol traffic as the client sees it (after TLS
// decrypts it on read, before TLS encrypts it on write) — that's what gets
// recorded here, tagged by direction with a fake client/transporter
// IP:port pair. UDP (rather than TCP) is used deliberately: its checksum
// is optional, so a valid-looking frame doesn't require simulating TCP
// sequence numbers or a real checksum at all.
package pcapwriter

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

const (
	magicMicroseconds = 0xa1b2c3d4
	versionMajor      = 2
	versionMinor      = 4
	linkTypeEthernet  = 1
	snapLen           = 1 << 18 // 256KiB, comfortably above MaxPayloadSize

	etherTypeIPv4 = 0x0800
	ipProtoUDP    = 17
)

// Direction tags which side of the connection a captured message travelled.
type Direction int

const (
	Outgoing Direction = iota // client -> transporter
	Incoming                  // transporter -> client
)

var (
	macClient       = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	macTransporter  = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}
	ipClient        = [4]byte{10, 0, 0, 1}
	ipTransporter   = [4]byte{10, 0, 0, 2}
	portClient      = uint16(1)
	portTransporter = uint16(2)
)

// Writer appends captured messages to an underlying io.Writer as pcap
// packet records, following a global header written once by NewWriter.
type Writer struct {
	out    io.Writer
	nextID uint16 // IPv4 identification field; cosmetic, just needs to vary
}

// NewWriter writes the pcap global header to out and returns a Writer ready
// to accept packets.
func NewWriter(out io.Writer) (*Writer, error) {
	header := make([]byte, 24)
	binary.LittleEndian.PutUint32(header[0:4], magicMicroseconds)
	binary.LittleEndian.PutUint16(header[4:6], versionMajor)
	binary.LittleEndian.PutUint16(header[6:8], versionMinor)
	// bytes 8:12 (GMT offset) and 12:16 (timestamp accuracy) are both
	// conventionally left at 0.
	binary.LittleEndian.PutUint32(header[16:20], snapLen)
	binary.LittleEndian.PutUint32(header[20:24], linkTypeEthernet)
	if _, err := out.Write(header); err != nil {
		return nil, fmt.Errorf("failed to write the pcap global header: %w", err)
	}
	return &Writer{out: out}, nil
}

// WritePacket records payload as one captured frame, travelling in
// direction, timestamped at ts.
func (w *Writer) WritePacket(direction Direction, payload []byte, ts time.Time) error {
	srcMAC, dstMAC := macClient, macTransporter
	srcIP, dstIP := ipClient, ipTransporter
	srcPort, dstPort := portClient, portTransporter
	if direction == Incoming {
		srcMAC, dstMAC = dstMAC, srcMAC
		srcIP, dstIP = dstIP, srcIP
		srcPort, dstPort = dstPort, srcPort
	}

	udp := buildUDP(srcPort, dstPort, payload)
	ip := w.buildIPv4(srcIP, dstIP, udp)
	frame := buildEthernet(srcMAC, dstMAC, ip)

	record := make([]byte, 16)
	binary.LittleEndian.PutUint32(record[0:4], uint32(ts.Unix()))
	binary.LittleEndian.PutUint32(record[4:8], uint32(ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(record[8:12], uint32(len(frame)))
	binary.LittleEndian.PutUint32(record[12:16], uint32(len(frame)))

	if _, err := w.out.Write(record); err != nil {
		return fmt.Errorf("failed to write a pcap packet record header: %w", err)
	}
	if _, err := w.out.Write(frame); err != nil {
		return fmt.Errorf("failed to write a pcap packet frame: %w", err)
	}
	return nil
}

func buildEthernet(srcMAC, dstMAC [6]byte, payload []byte) []byte {
	frame := make([]byte, 14+len(payload))
	copy(frame[0:6], dstMAC[:])
	copy(frame[6:12], srcMAC[:])
	binary.BigEndian.PutUint16(frame[12:14], etherTypeIPv4)
	copy(frame[14:], payload)
	return frame
}

func (w *Writer) buildIPv4(srcIP, dstIP [4]byte, payload []byte) []byte {
	header := make([]byte, 20)
	header[0] = 0x45 // version 4, IHL 5 (20 bytes, no options)
	header[1] = 0    // DSCP/ECN
	binary.BigEndian.PutUint16(header[2:4], uint16(20+len(payload)))
	binary.BigEndian.PutUint16(header[4:6], w.nextID)
	w.nextID++
	binary.BigEndian.PutUint16(header[6:8], 0) // flags/fragment offset
	header[8] = 64                             // TTL
	header[9] = ipProtoUDP
	binary.BigEndian.PutUint16(header[10:12], 0) // checksum, filled below
	copy(header[12:16], srcIP[:])
	copy(header[16:20], dstIP[:])
	binary.BigEndian.PutUint16(header[10:12], ipv4Checksum(header))

	return append(header, payload...)
}

func buildUDP(srcPort, dstPort uint16, payload []byte) []byte {
	header := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(header[0:2], srcPort)
	binary.BigEndian.PutUint16(header[2:4], dstPort)
	binary.BigEndian.PutUint16(header[4:6], uint16(8+len(payload)))
	binary.BigEndian.PutUint16(header[6:8], 0) // checksum: 0 = not computed, valid for UDP/IPv4
	copy(header[8:], payload)
	return header
}

// ipv4Checksum computes the standard IPv4 header checksum (RFC 791 §3.1):
// the ones-complement sum of the header's 16-bit words, taken with the
// checksum field itself as 0.
func ipv4Checksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(header); i += 2 {
		sum += uint32(header[i])<<8 | uint32(header[i+1])
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}
