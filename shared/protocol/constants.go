package protocol

const ProtocolVersion uint32 = 0x0001
const MaxPayloadSize uint32 = 0xF000
const HeaderSize uint32 = 0x000C //3 int size field

const (
	CommandConnect      uint32 = 0x0001
	CommandReconnect    uint32 = 0x0002
	CommandCreateRoom   uint32 = 0x0003
	CommandJoinRoom     uint32 = 0x0004
	CommandAdbTransport uint32 = 0x0006 //TODO: We should encrypt this command's payload
	// CommandGuestLeft is sent by the transporter to the room owner (no
	// payload, no response expected) when the guest disconnects from an
	// active room. The owner's own transporter connection is unaffected by
	// this, so it has no other way to learn the guest is gone.
	CommandGuestLeft uint32 = 0x0007
)

const CommandResponseMask uint32 = 0x1000
const CommandErrorResponseMask uint32 = 0x2000

const (
	ErrorUnknown              int = 0x0001
	ErrorProtocolNotSupported int = 0x0001
	ErrorAlreadyInRoom        int = 0x0002
	ErrorRoomNotFound         int = 0x0003
	ErrorFull                 int = 0x0004
	ErrorNoParticipant        int = 0x0005
	ErrorInvalidPayload       int = 0x0006
)
