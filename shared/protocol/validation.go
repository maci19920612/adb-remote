package protocol

import (
	"fmt"
)

func ExpectCommand(m *TransporterMessage, expectedCommand uint32) error {
	if command := m.Command(); command&expectedCommand == 0 {
		return fmt.Errorf("unexpected command: %x, expected: %x", command, expectedCommand)
	}
	return nil
}
