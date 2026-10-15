package protocol

// ProtocolVersion 2 added the end-to-end key exchange to the join payloads
// and encrypted CommandAdbTransport payloads (see client/e2e).
const ProtocolVersion uint32 = 0x0002
const MaxPayloadSize uint32 = 0xF000
const HeaderSize uint32 = 0x000C //3 int size field

// Exact sizes of the keys and signatures in the join payloads: an Ed25519
// identity public key, an X25519 ephemeral public key and an Ed25519
// signature.
const (
	IdentityKeySize  = 32
	EphemeralKeySize = 32
	SignatureSize    = 64
)

const (
	CommandConnect    uint32 = 0x0001
	CommandReconnect  uint32 = 0x0002
	CommandCreateRoom uint32 = 0x0003
	CommandJoinRoom   uint32 = 0x0004
	// CommandAdbTransport carries one ADB message between the room's owner
	// and guest, encrypted end to end (see client/e2e): the transporter
	// only routes it.
	CommandAdbTransport uint32 = 0x0006
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
	// ErrorLimitReached refuses a room or connection the transporter has
	// no capacity left for.
	ErrorLimitReached int = 0x0007
	// ErrorTooManyAttempts refuses a join request from an address that
	// failed too many recently.
	ErrorTooManyAttempts int = 0x0008
	// ErrorRoomExpired tells an owner its room was closed for staying
	// without a guest for too long.
	ErrorRoomExpired int = 0x0009
)
