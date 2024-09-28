package protocol

import (
	"adb-remote.maci.team/shared"
	"adb-remote.maci.team/shared/utils"
	"encoding/binary"
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

func CreateMessage() *TransporterMessage {
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

func (message *TransporterMessage) Command() uint32 {
	return ByteOrder.Uint32(message.commandBuffer)
}
func (message *TransporterMessage) PayloadLength() uint32 {
	return ByteOrder.Uint32(message.payloadLengthBuffer)
}
func (message *TransporterMessage) PayloadCRC32() uint32 {
	return ByteOrder.Uint32(message.payloadCrc32Buffer)
}

func (message *TransporterMessage) Read(reader *shared.TransportRead) error {
	length, err := (*reader).Read(message.headerBuffer)
	if err != nil {
		return err
	}
	if err := utils.EnsureLength(len(message.headerBuffer), length); err != nil {
		return err
	}
	payloadLength := message.PayloadLength()
	if payloadLength > 0 {
		length, err := (*reader).Read(message.payloadBuffer[:payloadLength])
		if err != nil {
			return err
		}
		if err := utils.EnsureLength(int(payloadLength), length); err != nil {
			return err
		}
	}
	return nil
}

func (message *TransporterMessage) Write(writer *shared.TransportWrite) error {
	length, err := (*writer).Write(message.headerBuffer)
	if err != nil {
		return err
	}
	if err := utils.EnsureLength(len(message.headerBuffer), length); err != nil {
		return err
	}
	payloadLength := message.PayloadLength()
	if payloadLength > 0 {
		length, err := (*writer).Write(message.payloadBuffer[:payloadLength])
		if err != nil {
			return err
		}
		if err := utils.EnsureLength(int(payloadLength), length); err != nil {
			return err
		}
	}
	return nil
}
