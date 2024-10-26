package adb

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"strings"
)

const (
	magicConstant    = 0xffffffff
	MaxPayloadLength = 0x1000
	HeaderSize       = 0x0018
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

type AdbMessage struct {
	command       []byte
	arg1          []byte
	arg2          []byte
	dataLength    []byte
	dataCRC32     []byte
	magic         []byte
	data          []byte
	headerBuffer  []byte
	messageBuffer []byte
}

func (c *AdbMessage) Command() uint32 {
	return binary.LittleEndian.Uint32(c.command)
}

func (c *AdbMessage) CommandStirng() string {
	stringCommandBytes := make([]byte, 4)
	binary.NativeEndian.PutUint32(stringCommandBytes, c.Command())
	return string(stringCommandBytes)
}

func (c *AdbMessage) Arg1() uint32 {
	return binary.LittleEndian.Uint32(c.arg1)
}

func (c *AdbMessage) Arg2() uint32 {
	return binary.LittleEndian.Uint32(c.arg2)
}

func (c *AdbMessage) DataLength() uint32 {
	return binary.LittleEndian.Uint32(c.dataLength)
}

func (c *AdbMessage) DataCRC32() uint32 {
	return binary.LittleEndian.Uint32(c.dataCRC32)
}

func (c *AdbMessage) Magic() uint32 {
	return binary.LittleEndian.Uint32(c.magic)
}

func (c *AdbMessage) DataString() string {
	return string(c.data[:c.DataLength()])
}

func (c *AdbMessage) Data() []byte {
	return c.data
}

func CreateMessage() *AdbMessage {
	messageBuffer := make([]byte, HeaderSize+MaxPayloadLength)
	return &AdbMessage{
		command:       messageBuffer[0:4],
		arg1:          messageBuffer[4:8],
		arg2:          messageBuffer[8:12],
		dataLength:    messageBuffer[12:16],
		dataCRC32:     messageBuffer[16:20],
		magic:         messageBuffer[20:24],
		data:          messageBuffer[HeaderSize:],
		headerBuffer:  messageBuffer[0:HeaderSize],
		messageBuffer: messageBuffer,
	}
}

func (c *AdbMessage) Read(reader *net.Conn) error {
	length, err := (*reader).Read(c.headerBuffer)
	if err != nil {
		return err
	}
	if length != HeaderSize {
		return fmt.Errorf("invalid message got on incoming stream, expected length: %d, actual length: %d", HeaderSize, length)
	}
	if err := validateCommand(c.Command()); err != nil {
		return err
	}
	if err := validateMagic(c.Command(), c.Magic()); err != nil {
		return err
	}
	length, err = (*reader).Read(c.data[:c.DataLength()])
	if err != nil {
		return err
	}
	if length != int(c.DataLength()) {
		return fmt.Errorf("invalid message got on the incoming stream, expected length: %d, actual length: %d", c.DataLength(), length)
	}
	if err := validateData(c.data[:c.DataLength()], c.DataCRC32()); err != nil {
		return err
	}
	return nil
}

func (c *AdbMessage) Write(writer *net.Conn) error {
	dataLength := int(c.DataLength())
	_, err := (*writer).Write(c.messageBuffer[:HeaderSize+dataLength])
	return err
}

func (c *AdbMessage) DumpParsed() string {
	dumpBuilder := strings.Builder{}
	columnSize := 15
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %s\n", columnSize, "Command:", c.CommandStirng()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Raw command:", c.Command()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Arg1:", c.Arg1()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Arg2:", c.Arg1()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %d\n", columnSize, "DataL:", c.DataLength()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "DataC:", c.DataCRC32()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Magic:", c.Magic()))

	if c.DataLength() > 0 {
		dumpBuilder.WriteString("Data:\n")
		dumpBuilder.WriteString(hex.Dump(c.data[:c.DataLength()]))
	}

	return dumpBuilder.String()
}

func (c *AdbMessage) Set(command uint32, arg1 uint32, arg2 uint32, data []byte) {
	binary.LittleEndian.PutUint32(c.command, command)
	binary.LittleEndian.PutUint32(c.arg1, arg1)
	binary.LittleEndian.PutUint32(c.arg2, arg2)
	binary.LittleEndian.PutUint32(c.dataLength, uint32(len(data)))
	binary.LittleEndian.PutUint32(c.dataCRC32, crc32.ChecksumIEEE(data))
	binary.LittleEndian.PutUint32(c.magic, command^magicConstant)
	copy(c.data, data)
}
