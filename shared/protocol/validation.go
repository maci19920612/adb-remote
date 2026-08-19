package protocol

import (
	"fmt"
	"hash/crc32"
)

// ExpectCommand reports an error unless m's command is exactly
// expectedCommand (including any CommandResponseMask/CommandErrorResponseMask
// bits the caller included). The base command values (CommandConnect,
// CommandJoinRoom, ...) are small sequential integers, not one-hot bit
// flags, so a bitwise membership test here would produce false positives
// between unrelated commands that happen to share a bit (e.g. CommandJoinRoom
// (0x4) and CommandAdbTransport (0x6)); exact equality is what every caller
// actually wants.
func ExpectCommand(m *TransporterMessage, expectedCommand uint32) error {
	if command := m.Command(); command != expectedCommand {
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
