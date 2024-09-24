package models

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
)

const (
	magicConstant    = 0xffffffff
	maxPayloadLength = 4 * 1024
	HeaderSize       = 6 * 4
)

const (
	CommandConnect uint32 = 0x4e584e43
	CommandSync    uint32 = 0x434e5953
	CommandOpen    uint32 = 0x4e45504f
	CommandOkay    uint32 = 0x59414b4f
	CommandClose   uint32 = 0x45534c43
	CommandWrite   uint32 = 0x45545257
)

func validateCommand(command uint32) error {
	switch command {
	case
		CommandConnect,
		CommandSync,
		CommandOpen,
		CommandOkay,
		CommandClose,
		CommandWrite:
		return nil
	}
	return errors.New(fmt.Sprintf("Invlaid command, not supported: %x", command))
}

func validateMagic(command uint32, magic uint32) error {
	expectedMagic := command ^ magicConstant
	if expectedMagic != magic {
		return errors.New(fmt.Sprintf("Invalid magic, expected: %x, actial: %x", expectedMagic, magic))
	}
	return nil
}

func validateData(data []byte, expectedCrc32 uint32) error {
	actualCrc32 := crc32.ChecksumIEEE(data)
	if actualCrc32 != expectedCrc32 {
		return errors.New(fmt.Sprintf("Invalid crc32, expected: %x, actual: %x", expectedCrc32, actualCrc32))
	}
	return nil
}

type TransportOut interface {
	Write(data []byte) error
}

type TransportIn interface {
	Read(data []byte) error
}

type AdbMessage struct {
	Command    uint32
	Arg1       uint32
	Arg2       uint32
	DataLength uint32
	DataCRC32  uint32
	Magic      uint32
	Data       []byte
}

func CreateMessage() *AdbMessage {
	return &AdbMessage{
		Data: make([]byte, maxPayloadLength),
	}
}

func (command *AdbMessage) ReadHeader(source []byte) error {
	if actualSize := len(source); actualSize < HeaderSize {
		return fmt.Errorf("Invalid source buffer length, expected: %d, actual: %d", HeaderSize, actualSize)
	}
	command.Command = binary.LittleEndian.Uint32(source[0:4])
	command.Arg1 = binary.LittleEndian.Uint32(source[4:8])
	command.Arg2 = binary.LittleEndian.Uint32(source[8:12])
	command.DataLength = binary.LittleEndian.Uint32(source[12:16])
	command.DataCRC32 = binary.LittleEndian.Uint32(source[16:20])
	command.Magic = binary.LittleEndian.Uint32(source[20:24])
	if err := validateCommand(command.Command); err != nil {
		return err
	}
	if err := validateMagic(command.Command, command.Magic); err != nil {
		return err
	}
	return nil
}

func (command *AdbMessage) ReadData(data []byte) error {
	//Here we should not reread this from a byte array
	targetDataSection := command.Data[0:command.DataLength]
	copy(targetDataSection, data)
	if err := validateData(targetDataSection, command.DataCRC32); err != nil {
		return err
	}
	return nil
}

func (command *AdbMessage) Write(target []byte) (int, error) {
	expectedSize := int(command.DataLength) + HeaderSize
	if actualSize := len(target); actualSize < expectedSize {
		return 0, fmt.Errorf("Invalid parameter buffer, can't fot the package into the buffer: expectedSize: %d, actualSize: %d", expectedSize, actualSize)
	}
	binary.LittleEndian.PutUint32(target[0:], command.Command)
	binary.LittleEndian.PutUint32(target[4:], command.Arg1)
	binary.LittleEndian.PutUint32(target[8:], command.Arg2)
	binary.LittleEndian.PutUint32(target[12:], command.DataLength)
	binary.LittleEndian.PutUint32(target[16:], command.DataCRC32)
	binary.LittleEndian.PutUint32(target[20:], command.Magic)
	copy(target[HeaderSize:HeaderSize+command.DataLength], command.Data[:])
	return expectedSize, nil
}

type messageDirection rune

const (
	MessageDirectionIn  messageDirection = '<'
	MessageDirectionOut messageDirection = '>'
)

func (command *AdbMessage) Dump(direction messageDirection) {
	delimiterBuilder := strings.Builder{}
	delimiterBuilder.WriteString("(client) ")
	delimiterSize := 10
	for i := 0; i < delimiterSize; i++ {
		delimiterBuilder.WriteRune(rune(direction))
	}
	delimiterBuilder.WriteString(" (server)")
	fmt.Println(delimiterBuilder.String())
	fmt.Printf("Command: \t%x\n", command.Command)
	fmt.Printf("Arg1: \t%d\n", command.Arg1)
	fmt.Printf("Arg2: \t%d\n", command.Arg2)
	fmt.Printf("DataL: \t%d\n", command.DataLength)
	fmt.Printf("DataC: \t%d\n", command.DataCRC32)
	fmt.Printf("Magic: \t%d\n", command.Magic)
	if command.DataLength > 0 {
		fmt.Println("Data: ")
		hex.Dump(command.Data[:command.DataLength])
	}
	fmt.Println(delimiterBuilder.String())
}

func (adbCommand *AdbMessage) SetHeader(command uint32, arg1 uint32, arg2 uint32, data []byte) error {
	if dataSize := len(data); dataSize > maxPayloadLength {
		return errors.New(fmt.Sprintf("Payload size not supported, expectedSize: %d, actialSize: %d", maxPayloadLength, dataSize))
	}
	adbCommand.Command = command
	adbCommand.Arg1 = arg1
	adbCommand.Arg2 = arg2
	adbCommand.Magic = command ^ magicConstant
	if data == nil {
		adbCommand.DataLength = uint32(len(data))
		adbCommand.DataCRC32 = crc32.ChecksumIEEE(data)
		targetReference := adbCommand.Data[:len(data)]
		copy(targetReference, data)
	} else {
		adbCommand.DataLength = 0
		adbCommand.DataCRC32 = 0
	}
	return nil
}
