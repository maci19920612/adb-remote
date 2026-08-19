package adb

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
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

var ErrMessageTooShort = errors.New("adb message shorter than the header size")

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
	return fmt.Errorf("invalid command, not supported: %x", command)
}

func validateMagic(command uint32, magic uint32) error {
	expectedMagic := command ^ magicConstant
	if expectedMagic != magic {
		return fmt.Errorf("invalid magic, expected: %x, actual: %x", expectedMagic, magic)
	}
	return nil
}

func validateData(data []byte, expectedCrc32 uint32) error {
	actualCrc32 := crc32.ChecksumIEEE(data)
	if actualCrc32 != expectedCrc32 {
		return fmt.Errorf("invalid crc32, expected: %x, actual: %x", expectedCrc32, actualCrc32)
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

func (c *AdbMessage) CommandString() string {
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
	return c.data[:c.DataLength()]
}

// Bytes returns the full wire representation (header + data) of the
// message, as it currently stands.
func (c *AdbMessage) Bytes() []byte {
	return c.messageBuffer[:HeaderSize+c.DataLength()]
}

func newMessageFromBuffer(messageBuffer []byte) *AdbMessage {
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

func CreateMessage() *AdbMessage {
	messageBuffer := make([]byte, HeaderSize+MaxPayloadLength)
	return newMessageFromBuffer(messageBuffer)
}

// DecodeMessage interprets an already-received, self-contained byte slice
// (header + data, with no trailing bytes) as an AdbMessage, validating its
// command, magic and data checksum. Unlike CreateMessage, it does not copy
// the input: the returned message shares the given backing array, so the
// caller must not reuse or mutate data while the message is in use.
func DecodeMessage(data []byte) (*AdbMessage, error) {
	if len(data) < HeaderSize {
		return nil, ErrMessageTooShort
	}
	m := newMessageFromBuffer(data)
	if err := validateCommand(m.Command()); err != nil {
		return nil, err
	}
	if err := validateMagic(m.Command(), m.Magic()); err != nil {
		return nil, err
	}
	dataLength := m.DataLength()
	if HeaderSize+dataLength > uint32(len(data)) {
		return nil, fmt.Errorf("declared data length %d exceeds the available buffer (%d bytes)", dataLength, len(data)-HeaderSize)
	}
	if err := validateData(m.data[:dataLength], m.DataCRC32()); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *AdbMessage) Read(reader io.Reader) error {
	_, err := io.ReadFull(reader, c.headerBuffer)
	if err != nil {
		return err
	}
	if err := validateCommand(c.Command()); err != nil {
		return err
	}
	if err := validateMagic(c.Command(), c.Magic()); err != nil {
		return err
	}
	dataLength := c.DataLength()
	if dataLength > MaxPayloadLength {
		return fmt.Errorf("declared data length %d exceeds the maximum allowed payload length (%d)", dataLength, MaxPayloadLength)
	}
	_, err = io.ReadFull(reader, c.data[:dataLength])
	if err != nil {
		return err
	}
	if err := validateData(c.data[:dataLength], c.DataCRC32()); err != nil {
		return err
	}
	return nil
}

func (c *AdbMessage) Write(writer io.Writer) error {
	_, err := writer.Write(c.Bytes())
	return err
}

func (c *AdbMessage) DumpParsed() string {
	dumpBuilder := strings.Builder{}
	columnSize := 15
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %s\n", columnSize, "Command:", c.CommandString()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Raw command:", c.Command()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Arg1:", c.Arg1()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Arg2:", c.Arg2()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %d\n", columnSize, "DataL:", c.DataLength()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "DataC:", c.DataCRC32()))
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "Magic:", c.Magic()))

	if c.DataLength() > 0 {
		dumpBuilder.WriteString("Data:\n")
		dumpBuilder.WriteString(hex.Dump(c.data[:c.DataLength()]))
	}

	return dumpBuilder.String()
}

func (c *AdbMessage) Set(command uint32, arg1 uint32, arg2 uint32, data []byte) error {
	if uint32(len(data)) > MaxPayloadLength {
		return fmt.Errorf("data length %d exceeds the maximum allowed payload length (%d)", len(data), MaxPayloadLength)
	}
	binary.LittleEndian.PutUint32(c.command, command)
	binary.LittleEndian.PutUint32(c.arg1, arg1)
	binary.LittleEndian.PutUint32(c.arg2, arg2)
	binary.LittleEndian.PutUint32(c.dataLength, uint32(len(data)))
	binary.LittleEndian.PutUint32(c.dataCRC32, crc32.ChecksumIEEE(data))
	binary.LittleEndian.PutUint32(c.magic, command^magicConstant)
	copy(c.data, data)
	return nil
}
