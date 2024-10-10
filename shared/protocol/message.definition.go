package protocol

import (
	"adb-remote.maci.team/shared/utils"
	"encoding/binary"
	"net"
	"sync"
)

var ByteOrder binary.ByteOrder = binary.LittleEndian

type TransporterMessage struct {
	commandBuffer       []byte
	payloadLengthBuffer []byte
	payloadCrc32Buffer  []byte
	payloadBuffer       []byte
	headerBuffer        []byte
	messageBuffer       []byte
	mutex               *sync.Mutex
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
		mutex:               new(sync.Mutex),
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

func (message *TransporterMessage) SetDirectCommand(command uint32) {
	message.mutex.Lock()
	defer message.mutex.Unlock()
	ByteOrder.PutUint32(message.commandBuffer, command)
}

func (message *TransporterMessage) SetResponseCommand(command uint32) {
	message.SetDirectCommand(command | CommandResponseMask)
}

func (message *TransporterMessage) SetErrorResponseCommand(command uint32) {
	message.SetDirectCommand(command | CommandErrorResponseMask)
}

func (message *TransporterMessage) Read(reader *net.Conn) error {
	message.mutex.Lock()
	defer message.mutex.Unlock()
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

func (message *TransporterMessage) Write(writer *net.Conn) error {
	message.mutex.Lock()
	defer message.mutex.Unlock()
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
