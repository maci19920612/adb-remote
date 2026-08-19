package protocol

import (
	"errors"
	"fmt"
)

var ErrPayloadTooLarge = errors.New("payload length exceeds the maximum allowed payload size")

func EnsureLength(expected int, actual int) error {
	if expected != actual {
		return fmt.Errorf("ensure length failed, expected: %d, actual: %d", expected, actual)
	}
	return nil
}

func EnsureIntLength(actual int) error {
	return EnsureLength(4, actual)
}

func (m *TransporterMessage) IsError() bool {
	return m.Command()&CommandErrorResponseMask != 0
}
