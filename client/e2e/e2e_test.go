package e2e

import (
	"adb-remote.maci.team/client/identity"
	"bytes"
	"path/filepath"
	"testing"
)

func testIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Load(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatalf("failed to create a test identity: %s", err)
	}
	return id
}

// handshake runs the whole key exchange between guest and owner for
// roomId and returns both sessions.
func handshake(t *testing.T, guest *identity.Identity, owner *identity.Identity, roomId string) (guestSession *Session, ownerSession *Session) {
	t.Helper()
	guestHandshake, err := StartGuest(guest, roomId)
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	if err := VerifyOffer(roomId, guest.PublicKey, guestHandshake.Offer()); err != nil {
		t.Fatalf("VerifyOffer failed: %s", err)
	}
	answer, ownerSession, err := Accept(owner, roomId, guest.PublicKey, guestHandshake.Offer())
	if err != nil {
		t.Fatalf("Accept failed: %s", err)
	}
	guestSession, err = guestHandshake.Finish(owner.PublicKey, answer)
	if err != nil {
		t.Fatalf("Finish failed: %s", err)
	}
	return guestSession, ownerSession
}

func expectRoundTrip(t *testing.T, from *Session, to *Session, plaintext []byte) {
	t.Helper()
	sealed := from.Seal(nil, plaintext)
	if len(sealed) != len(plaintext)+Overhead {
		t.Fatalf("expected %d sealed bytes, got %d", len(plaintext)+Overhead, len(sealed))
	}
	if len(plaintext) > 0 && bytes.Contains(sealed, plaintext) {
		t.Fatalf("expected the sealed message not to contain the plaintext")
	}
	opened, err := to.Open(nil, sealed)
	if err != nil {
		t.Fatalf("Open failed: %s", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("expected %q, got %q", plaintext, opened)
	}
}

func TestSessionsCarryTrafficBothWays(t *testing.T) {
	guestSession, ownerSession := handshake(t, testIdentity(t), testIdentity(t), "ROOM-0001")

	for i := 0; i < 3; i++ {
		expectRoundTrip(t, guestSession, ownerSession, []byte("OPEN shell:id"))
		expectRoundTrip(t, ownerSession, guestSession, []byte("WRTE uid=2000(shell)"))
	}
	expectRoundTrip(t, guestSession, ownerSession, nil)
}

func TestOpenDecryptsInPlace(t *testing.T) {
	guestSession, ownerSession := handshake(t, testIdentity(t), testIdentity(t), "ROOM-0001")

	sealed := guestSession.Seal(nil, []byte("in place"))
	opened, err := ownerSession.Open(sealed[:0], sealed)
	if err != nil {
		t.Fatalf("Open failed: %s", err)
	}
	if string(opened) != "in place" {
		t.Fatalf("expected %q, got %q", "in place", opened)
	}
}

// TestOfferFromAnotherIdentityIsRejected covers a relay presenting a
// genuine guest's public key (so the fingerprint matches) with a key
// exchange of its own: the offer has to be signed by that very key.
func TestOfferFromAnotherIdentityIsRejected(t *testing.T) {
	guest := testIdentity(t)
	impostor := testIdentity(t)
	impostorHandshake, err := StartGuest(impostor, "ROOM-0001")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}

	if err := VerifyOffer("ROOM-0001", guest.PublicKey, impostorHandshake.Offer()); err == nil {
		t.Fatalf("expected an offer signed by another identity to be rejected")
	}
	if _, _, err := Accept(testIdentity(t), "ROOM-0001", guest.PublicKey, impostorHandshake.Offer()); err == nil {
		t.Fatalf("expected Accept to reject an offer signed by another identity")
	}
}

func TestOfferForAnotherRoomIsRejected(t *testing.T) {
	guest := testIdentity(t)
	guestHandshake, err := StartGuest(guest, "ROOM-0001")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	if err := VerifyOffer("ROOM-0002", guest.PublicKey, guestHandshake.Offer()); err == nil {
		t.Fatalf("expected an offer made for another room to be rejected")
	}
}

// TestAnswerWithASubstitutedKeyIsRejected covers a relay swapping the
// owner's ephemeral key for its own while forwarding the owner's genuine
// identity key: the answer has to be signed by that identity.
func TestAnswerWithASubstitutedKeyIsRejected(t *testing.T) {
	guest := testIdentity(t)
	owner := testIdentity(t)
	guestHandshake, err := StartGuest(guest, "ROOM-0001")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	answer, _, err := Accept(owner, "ROOM-0001", guest.PublicKey, guestHandshake.Offer())
	if err != nil {
		t.Fatalf("Accept failed: %s", err)
	}

	relayHandshake, err := StartGuest(testIdentity(t), "ROOM-0001")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	substituted := KeyExchange{EphemeralKey: relayHandshake.Offer().EphemeralKey, Signature: answer.Signature}
	if _, err := guestHandshake.Finish(owner.PublicKey, substituted); err == nil {
		t.Fatalf("expected an answer with a substituted ephemeral key to be rejected")
	}
	if _, err := guestHandshake.Finish(testIdentity(t).PublicKey, answer); err == nil {
		t.Fatalf("expected an answer checked against another owner identity to be rejected")
	}
}

// TestAnswerToAnotherOfferIsRejected covers a relay replaying an owner's
// answer from an earlier join: the answer is bound to the offer it
// answers.
func TestAnswerToAnotherOfferIsRejected(t *testing.T) {
	guest := testIdentity(t)
	owner := testIdentity(t)
	earlier, err := StartGuest(guest, "ROOM-0001")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	earlierAnswer, _, err := Accept(owner, "ROOM-0001", guest.PublicKey, earlier.Offer())
	if err != nil {
		t.Fatalf("Accept failed: %s", err)
	}

	current, err := StartGuest(guest, "ROOM-0001")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	if _, err := current.Finish(owner.PublicKey, earlierAnswer); err == nil {
		t.Fatalf("expected an answer to an earlier offer to be rejected")
	}
}

func TestMalformedKeyExchangeIsRejected(t *testing.T) {
	guest := testIdentity(t)
	guestHandshake, err := StartGuest(guest, "ROOM-0001")
	if err != nil {
		t.Fatalf("StartGuest failed: %s", err)
	}
	offer := guestHandshake.Offer()
	for name, malformed := range map[string]KeyExchange{
		"short ephemeral key": {EphemeralKey: offer.EphemeralKey[:8], Signature: offer.Signature},
		"short signature":     {EphemeralKey: offer.EphemeralKey, Signature: offer.Signature[:8]},
		"empty":               {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyOffer("ROOM-0001", guest.PublicKey, malformed); err == nil {
				t.Fatalf("expected a malformed offer to be rejected")
			}
		})
	}
	if err := VerifyOffer("ROOM-0001", guest.PublicKey[:8], offer); err == nil {
		t.Fatalf("expected a malformed guest identity key to be rejected")
	}
}

func TestTamperedMessageIsRejected(t *testing.T) {
	guestSession, ownerSession := handshake(t, testIdentity(t), testIdentity(t), "ROOM-0001")

	sealed := guestSession.Seal(nil, []byte("WRTE data"))
	sealed[0] ^= 0x01
	if _, err := ownerSession.Open(nil, sealed); err == nil {
		t.Fatalf("expected a tampered message to be rejected")
	}
}

// TestReplayedOrReorderedMessageIsRejected checks every message can be
// opened exactly once, in order: the relay can't repeat, drop or reorder
// ADB messages unnoticed.
func TestReplayedOrReorderedMessageIsRejected(t *testing.T) {
	guestSession, ownerSession := handshake(t, testIdentity(t), testIdentity(t), "ROOM-0001")

	first := guestSession.Seal(nil, []byte("first"))
	second := guestSession.Seal(nil, []byte("second"))
	if _, err := ownerSession.Open(nil, second); err == nil {
		t.Fatalf("expected a message opened out of order to be rejected")
	}
	if _, err := ownerSession.Open(nil, first); err != nil {
		t.Fatalf("expected the first message to open, got %s", err)
	}
	if _, err := ownerSession.Open(nil, first); err == nil {
		t.Fatalf("expected a replayed message to be rejected")
	}
}

// TestDirectionsUseSeparateKeys checks a message can't be reflected back
// to its sender as if the other side had sent it.
func TestDirectionsUseSeparateKeys(t *testing.T) {
	guestSession, _ := handshake(t, testIdentity(t), testIdentity(t), "ROOM-0001")

	sealed := guestSession.Seal(nil, []byte("reflected"))
	if _, err := guestSession.Open(nil, sealed); err == nil {
		t.Fatalf("expected a message reflected to its sender to be rejected")
	}
}
