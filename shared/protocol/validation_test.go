package protocol

import "testing"

func TestExpectCommandAccepts(t *testing.T) {
	m := CreateTransporterMessage()
	m.SetResponseCommand(CommandJoinRoom)
	if err := ExpectCommand(m, CommandJoinRoom|CommandResponseMask); err != nil {
		t.Fatalf("expected the exact command to be accepted, got: %s", err)
	}
}

func TestExpectCommandRejectsMismatch(t *testing.T) {
	m := CreateTransporterMessage()
	m.SetDirectCommand(CommandCreateRoom)
	if err := ExpectCommand(m, CommandJoinRoom); err == nil {
		t.Fatalf("expected a mismatched command to be rejected")
	}
}

// TestExpectCommandDoesNotFalsePositiveOnSharedBits is a regression test:
// the base command values (Connect=1, Reconnect=2, CreateRoom=3,
// JoinRoom=4, AdbTransport=6) are small sequential integers, not one-hot
// bit flags, so CommandAdbTransport (0b110) and CommandJoinRoom (0b100)
// share a bit. An earlier bitwise-AND implementation of ExpectCommand
// treated an incoming CommandAdbTransport message as satisfying an
// "expect CommandJoinRoom" check, which — live, in the owner-side relay —
// caused every relayed ADB message to be misinterpreted as a bogus room
// join request once a real ADB session was underway.
func TestExpectCommandDoesNotFalsePositiveOnSharedBits(t *testing.T) {
	m := CreateTransporterMessage()
	m.SetDirectCommand(CommandAdbTransport)
	if err := ExpectCommand(m, CommandJoinRoom); err == nil {
		t.Fatalf("CommandAdbTransport must not satisfy an expectation of CommandJoinRoom")
	}
	if err := ExpectCommand(m, CommandCreateRoom); err == nil {
		t.Fatalf("CommandAdbTransport must not satisfy an expectation of CommandCreateRoom")
	}
}
