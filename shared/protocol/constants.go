package protocol

const ProtocolVersion uint32 = 0x0001
const MaxPayloadSize uint32 = 0xF000
const HeaderSize uint32 = 0x000C //3 int size field

const (
	CommandConnect           uint32 = 0x0001
	CommandReconnect         uint32 = 0x0002
	CommandCreateRoom        uint32 = 0x0003
	CommandConnectRoom       uint32 = 0x0004
	CommandConnectRoomResult uint32 = 0x0005
	CommandAdbTransport      uint32 = 0x0006 //TODO: We should encrypt this command's payload
)

const CommandResponseMask uint32 = 0x1000
const CommandErrorResponseMask uint32 = 0x2000

const (
	ErrorProtocolNotSupported int = 0x0001
)
