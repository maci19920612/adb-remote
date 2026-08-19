package protocol

import (
	"encoding/binary"
	"io"
)

var ByteOrder binary.ByteOrder = binary.LittleEndian

type TransporterMessage struct {
	commandBuffer       []byte
	payloadLengthBuffer []byte
	payloadCrc32Buffer  []byte
	payloadBuffer       []byte
	headerBuffer        []byte
	messageBuffer       []byte
}

func CreateTransporterMessage() *TransporterMessage {
	messageBuffer := make([]byte, MaxPayloadSize+4*3)
	return &TransporterMessage{
		commandBuffer:       messageBuffer[0:4],
		payloadLengthBuffer: messageBuffer[4:8],
		payloadCrc32Buffer:  messageBuffer[8:12],
		payloadBuffer:       messageBuffer[12:],
		headerBuffer:        messageBuffer[0:12],
		messageBuffer:       messageBuffer,
	}
}

func (m *TransporterMessage) Command() uint32 {
	return ByteOrder.Uint32(m.commandBuffer)
}
func (m *TransporterMessage) PayloadLength() uint32 {
	return ByteOrder.Uint32(m.payloadLengthBuffer)
}
func (m *TransporterMessage) PayloadCRC32() uint32 {
	return ByteOrder.Uint32(m.payloadCrc32Buffer)
}

// WireSize returns the total number of bytes this message occupies on the
// wire (header plus payload), i.e. exactly what a Read/Write call transfers.
func (m *TransporterMessage) WireSize() uint32 {
	return HeaderSize + m.PayloadLength()
}

// Payload returns the raw payload bytes currently held by the message,
// sliced to the length recorded in the header.
func (m *TransporterMessage) Payload() []byte {
	return m.payloadBuffer[:m.PayloadLength()]
}

func (m *TransporterMessage) SetDirectCommand(command uint32) {
	ByteOrder.PutUint32(m.commandBuffer, command)
}

func (m *TransporterMessage) SetResponseCommand(command uint32) {
	m.SetDirectCommand(command | CommandResponseMask)
}

func (m *TransporterMessage) SetErrorResponseCommand(command uint32) {
	m.SetDirectCommand(command | CommandErrorResponseMask)
}

/*
This only used when we want to write the payload form a separate buffer
*/
func (m *TransporterMessage) SetHeader(command uint32, payloadLength uint32, payloadCrc32 uint32) {
	ByteOrder.PutUint32(m.commandBuffer, command)
	ByteOrder.PutUint32(m.payloadLengthBuffer, payloadLength)
	ByteOrder.PutUint32(m.payloadCrc32Buffer, payloadCrc32)
}

func (m *TransporterMessage) Read(reader io.Reader) error {
	length, err := io.ReadFull(reader, m.headerBuffer)
	if err != nil {
		return err
	}
	if err := EnsureLength(len(m.headerBuffer), length); err != nil {
		return err
	}
	payloadLength := m.PayloadLength()
	if payloadLength > MaxPayloadSize {
		return ErrPayloadTooLarge
	}
	if payloadLength > 0 {
		length, err := io.ReadFull(reader, m.payloadBuffer[:payloadLength])
		if err != nil {
			return err
		}
		if err := EnsureLength(int(payloadLength), length); err != nil {
			return err
		}
		if err := validatePayloadCRC32(m.payloadBuffer[:payloadLength], m.PayloadCRC32()); err != nil {
			return err
		}
	}
	return nil
}

func (m *TransporterMessage) Write(writer io.Writer) error {
	length, err := writer.Write(m.headerBuffer)
	if err != nil {
		return err
	}
	if err := EnsureLength(len(m.headerBuffer), length); err != nil {
		return err
	}
	payloadLength := m.PayloadLength()
	if payloadLength > 0 {
		length, err := writer.Write(m.payloadBuffer[:payloadLength])
		if err != nil {
			return err
		}
		if err := EnsureLength(int(payloadLength), length); err != nil {
			return err
		}
	}
	return nil
}
