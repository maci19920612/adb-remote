package adb

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	magicConstant = 0xffffffff
	// MaxPayloadLength is our advertised MAXDATA. The real ADB wire
	// protocol allows much larger values (modern adb clients offer up to
	// 1MiB), but this must stay comfortably within
	// protocol.MaxPayloadSize, since a whole ADB message (header + data)
	// is embedded verbatim as a TransporterMessage payload when relayed.
	MaxPayloadLength = 0x8000
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

// checksum reproduces the ADB wire protocol's "data_check" field: despite
// the field name adb itself uses (and despite what a casual reading of the
// protocol suggests), it is not a CRC32 — it is the sum of the payload
// bytes as unsigned values, truncated to 32 bits.
func checksum(data []byte) uint32 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return sum
}

type AdbMessage struct {
	command       []byte
	arg1          []byte
	arg2          []byte
	dataLength    []byte
	dataChecksum  []byte
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

func (c *AdbMessage) DataChecksum() uint32 {
	return binary.LittleEndian.Uint32(c.dataChecksum)
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
		dataChecksum:  messageBuffer[16:20],
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
// command and magic. Unlike CreateMessage, it does not copy the input: the
// returned message shares the given backing array, so the caller must not
// reuse or mutate data while the message is in use.
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
	// The data_check field is not validated: real adb clients only compute
	// a real checksum for the initial CNXN and send a literal 0 for every
	// message after that (protocol version A_VERSION_SKIP_CHECKSUM and
	// above, which is effectively every adb build in current use), and
	// they likewise do not validate what we send them.
	_, err = io.ReadFull(reader, c.data[:dataLength])
	if err != nil {
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
	dumpBuilder.WriteString(fmt.Sprintf("%-*s %x\n", columnSize, "DataC:", c.DataChecksum()))
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
	binary.LittleEndian.PutUint32(c.dataChecksum, checksum(data))
	binary.LittleEndian.PutUint32(c.magic, command^magicConstant)
	copy(c.data, data)
	return nil
}
