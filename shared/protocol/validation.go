package protocol

import (
	"fmt"
	"hash/crc32"
)

func ExpectCommand(m *TransporterMessage, expectedCommand uint32) error {
	if command := m.Command(); command&expectedCommand == 0 {
		return fmt.Errorf("unexpected command: %x, expected: %x", command, expectedCommand)
	}
	return nil
}

func validatePayloadCRC32(payload []byte, expectedCrc32 uint32) error {
	actualCrc32 := crc32.ChecksumIEEE(payload)
	if actualCrc32 != expectedCrc32 {
		return fmt.Errorf("invalid payload crc32, expected: %x, actual: %x", expectedCrc32, actualCrc32)
	}
	return nil
}
