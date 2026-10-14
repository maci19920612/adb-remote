package capture

import (
	"encoding/binary"
	"time"
)

// pcapng block encoders (see the pcapng spec, draft-ietf-opsawg-pcapng).
// Every block is built whole in memory so the capture can append it with a
// single Write, keeping the file well-formed up to the last complete block
// even if the process is killed mid-capture.
const (
	blockTypeSectionHeader        = 0x0A0D0D0A
	blockTypeInterfaceDescription = 0x00000001
	blockTypeEnhancedPacket       = 0x00000006
	blockTypeDecryptionSecrets    = 0x0000000A

	byteOrderMagic = 0x1A2B3C4D

	// linkTypeRaw (LINKTYPE_RAW) means every packet starts directly with
	// an IPv4 or IPv6 header, so no fake link-layer header is needed.
	linkTypeRaw = 101

	// secretsTypeTLSKeyLog marks a Decryption Secrets Block holding NSS
	// key log lines (the SSLKEYLOGFILE format), which Wireshark applies
	// automatically to decrypt the TLS streams in the same file.
	secretsTypeTLSKeyLog = 0x544c534b
)

func sectionHeaderBlock() []byte {
	body := make([]byte, 16)
	binary.LittleEndian.PutUint32(body[0:4], byteOrderMagic)
	binary.LittleEndian.PutUint16(body[4:6], 1) // major version
	binary.LittleEndian.PutUint16(body[6:8], 0) // minor version
	// -1: section length not specified, since the file is written as a
	// stream and the length isn't known up front.
	binary.LittleEndian.PutUint64(body[8:16], 0xFFFFFFFFFFFFFFFF)
	return block(blockTypeSectionHeader, body)
}

func interfaceDescriptionBlock() []byte {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint16(body[0:2], linkTypeRaw)
	// body[2:4] is reserved and body[4:8] is the snap length, 0 meaning
	// unlimited. With no if_tsresol option, timestamps are in microseconds.
	return block(blockTypeInterfaceDescription, body)
}

func enhancedPacketBlock(ts time.Time, packet []byte) []byte {
	body := make([]byte, 20, 20+padded(len(packet)))
	micros := uint64(ts.UnixMicro())
	binary.LittleEndian.PutUint32(body[0:4], 0) // interface id
	binary.LittleEndian.PutUint32(body[4:8], uint32(micros>>32))
	binary.LittleEndian.PutUint32(body[8:12], uint32(micros))
	binary.LittleEndian.PutUint32(body[12:16], uint32(len(packet))) // captured length
	binary.LittleEndian.PutUint32(body[16:20], uint32(len(packet))) // original length
	body = append(body, packet...)
	return block(blockTypeEnhancedPacket, padTo4(body))
}

func decryptionSecretsBlock(secrets []byte) []byte {
	body := make([]byte, 8, 8+padded(len(secrets)))
	binary.LittleEndian.PutUint32(body[0:4], secretsTypeTLSKeyLog)
	binary.LittleEndian.PutUint32(body[4:8], uint32(len(secrets)))
	body = append(body, secrets...)
	return block(blockTypeDecryptionSecrets, padTo4(body))
}

// block wraps body (already padded to 32 bits) with the block type and the
// total block length, which pcapng repeats at both ends of every block.
func block(blockType uint32, body []byte) []byte {
	totalLength := uint32(12 + len(body))
	out := make([]byte, 0, totalLength)
	out = binary.LittleEndian.AppendUint32(out, blockType)
	out = binary.LittleEndian.AppendUint32(out, totalLength)
	out = append(out, body...)
	return binary.LittleEndian.AppendUint32(out, totalLength)
}

func padded(length int) int {
	return (length + 3) &^ 3
}

func padTo4(data []byte) []byte {
	return append(data, make([]byte, padded(len(data))-len(data))...)
}
