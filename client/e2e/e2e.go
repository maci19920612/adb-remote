// Package e2e encrypts the ADB traffic between a room's owner and guest end
// to end, so the transporter in between (or anyone who gets between a
// client and the transporter, whose TLS certificate nobody verifies) can
// route it but neither read nor change it.
//
// The keys come from an X25519 exchange that rides along with the room
// join: the guest's half goes with its join request, the owner's half with
// its acceptance. Each half is signed with that side's identity key (see
// client/identity), the very key whose fingerprint the two operators
// compare out of band. A relay that wanted to read the traffic would have
// to swap in key exchanges of its own, which it can't sign with the
// genuine identity keys; and swapping the identity keys too changes the
// fingerprints the operators see.
//
// Each direction then has its own AES-256-GCM key. Nonces are a message
// counter, never sent: the transporter relays messages in order over TCP,
// so every message has to be opened exactly once and in order, and one
// that was dropped, repeated, reordered or altered is refused.
package e2e

import (
	"adb-remote.maci.team/client/identity"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
)

// Labels that keep each signature and derived key bound to its purpose.
const (
	offerLabel        = "adb-remote e2e v1 guest offer"
	answerLabel       = "adb-remote e2e v1 owner answer"
	transcriptLabel   = "adb-remote e2e v1 session"
	guestToOwnerLabel = "adb-remote e2e v1 guest to owner"
	ownerToGuestLabel = "adb-remote e2e v1 owner to guest"
)

const (
	// EphemeralKeySize and SignatureSize are the exact sizes of a
	// KeyExchange's fields.
	EphemeralKeySize = 32
	SignatureSize    = ed25519.SignatureSize
	// Overhead is how many bytes Seal adds to a message.
	Overhead = 16

	keySize = 32
)

var ErrDecryption = errors.New("the message failed to decrypt: it was altered, replayed, reordered, or a message before it was lost")

// KeyExchange is one side's half of the key exchange: an X25519 public key
// signed with that side's identity key.
type KeyExchange struct {
	EphemeralKey []byte
	Signature    []byte
}

// GuestHandshake is the guest's side of the key exchange, between sending
// its offer and receiving the owner's answer.
type GuestHandshake struct {
	roomId    string
	guestKey  ed25519.PublicKey
	ephemeral *ecdh.PrivateKey
	offer     KeyExchange
}

// StartGuest begins the key exchange for joining roomId as guest; Offer
// goes along with the join request.
func StartGuest(guest *identity.Identity, roomId string) (*GuestHandshake, error) {
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	ephemeralKey := ephemeral.PublicKey().Bytes()
	return &GuestHandshake{
		roomId:    roomId,
		guestKey:  guest.PublicKey,
		ephemeral: ephemeral,
		offer: KeyExchange{
			EphemeralKey: ephemeralKey,
			Signature:    ed25519.Sign(guest.PrivateKey, offerMessage(roomId, ephemeralKey)),
		},
	}, nil
}

// Offer returns the guest's half of the key exchange.
func (h *GuestHandshake) Offer() KeyExchange {
	return h.offer
}

// Finish checks the owner's answer was signed by ownerKey, for this very
// offer, and returns the guest's session.
func (h *GuestHandshake) Finish(ownerKey ed25519.PublicKey, answer KeyExchange) (*Session, error) {
	if err := checkKeyExchange(ownerKey, answer); err != nil {
		return nil, fmt.Errorf("invalid answer from the room owner: %w", err)
	}
	if !ed25519.Verify(ownerKey, answerMessage(h.roomId, h.guestKey, h.offer.EphemeralKey, answer.EphemeralKey), answer.Signature) {
		return nil, errors.New("the room owner's answer isn't signed by the owner's identity key")
	}
	shared, err := sharedSecret(h.ephemeral, answer.EphemeralKey)
	if err != nil {
		return nil, err
	}
	transcript := transcriptHash(h.roomId, h.guestKey, ownerKey, h.offer.EphemeralKey, answer.EphemeralKey)
	return newSession(shared, transcript, guestToOwnerLabel, ownerToGuestLabel)
}

// VerifyOffer checks a guest's offer to join roomId was signed by guestKey,
// the identity key whose fingerprint the owner is shown.
func VerifyOffer(roomId string, guestKey ed25519.PublicKey, offer KeyExchange) error {
	if err := checkKeyExchange(guestKey, offer); err != nil {
		return fmt.Errorf("invalid offer from the guest: %w", err)
	}
	if !ed25519.Verify(guestKey, offerMessage(roomId, offer.EphemeralKey), offer.Signature) {
		return errors.New("the guest's offer isn't signed by the guest's identity key")
	}
	return nil
}

// Accept answers a guest's offer to join roomId, after verifying it (see
// VerifyOffer), and returns the owner's answer and session.
func Accept(owner *identity.Identity, roomId string, guestKey ed25519.PublicKey, offer KeyExchange) (KeyExchange, *Session, error) {
	if err := VerifyOffer(roomId, guestKey, offer); err != nil {
		return KeyExchange{}, nil, err
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyExchange{}, nil, err
	}
	shared, err := sharedSecret(ephemeral, offer.EphemeralKey)
	if err != nil {
		return KeyExchange{}, nil, err
	}
	ephemeralKey := ephemeral.PublicKey().Bytes()
	answer := KeyExchange{
		EphemeralKey: ephemeralKey,
		Signature:    ed25519.Sign(owner.PrivateKey, answerMessage(roomId, guestKey, offer.EphemeralKey, ephemeralKey)),
	}
	transcript := transcriptHash(roomId, guestKey, owner.PublicKey, offer.EphemeralKey, ephemeralKey)
	session, err := newSession(shared, transcript, ownerToGuestLabel, guestToOwnerLabel)
	if err != nil {
		return KeyExchange{}, nil, err
	}
	return answer, session, nil
}

func checkKeyExchange(identityKey ed25519.PublicKey, exchange KeyExchange) error {
	if len(identityKey) != ed25519.PublicKeySize {
		return fmt.Errorf("identity key: expected %d bytes, got %d", ed25519.PublicKeySize, len(identityKey))
	}
	if len(exchange.EphemeralKey) != EphemeralKeySize {
		return fmt.Errorf("ephemeral key: expected %d bytes, got %d", EphemeralKeySize, len(exchange.EphemeralKey))
	}
	if len(exchange.Signature) != SignatureSize {
		return fmt.Errorf("signature: expected %d bytes, got %d", SignatureSize, len(exchange.Signature))
	}
	return nil
}

func sharedSecret(own *ecdh.PrivateKey, peerKey []byte) ([]byte, error) {
	peer, err := ecdh.X25519().NewPublicKey(peerKey)
	if err != nil {
		return nil, err
	}
	// Fails for a low-order peer key, which would make the secret known.
	return own.ECDH(peer)
}

// offerMessage is what the guest signs: its ephemeral key, for this room.
func offerMessage(roomId string, guestEphemeralKey []byte) []byte {
	return concat(offerLabel, []byte(roomId), guestEphemeralKey)
}

// answerMessage is what the owner signs: its ephemeral key, in answer to
// this guest's offer for this room.
func answerMessage(roomId string, guestKey ed25519.PublicKey, guestEphemeralKey []byte, ownerEphemeralKey []byte) []byte {
	return concat(answerLabel, []byte(roomId), guestKey, guestEphemeralKey, ownerEphemeralKey)
}

func transcriptHash(roomId string, guestKey ed25519.PublicKey, ownerKey ed25519.PublicKey, guestEphemeralKey []byte, ownerEphemeralKey []byte) []byte {
	sum := sha256.Sum256(concat(transcriptLabel, []byte(roomId), guestKey, ownerKey, guestEphemeralKey, ownerEphemeralKey))
	return sum[:]
}

// concat joins label and fields, each field prefixed with its length, so
// no two different field lists encode the same.
func concat(label string, fields ...[]byte) []byte {
	encoded := []byte(label)
	for _, field := range fields {
		encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(field)))
		encoded = append(encoded, field...)
	}
	return encoded
}

// Session encrypts what one side sends and decrypts what it receives.
// Messages must be sealed in the order they are sent and opened in the
// order they arrive; Seal and Open are each safe for concurrent use, but
// a caller that seals from several goroutines must also send in the same
// order it sealed.
type Session struct {
	sendMutex   sync.Mutex
	send        cipher.AEAD
	sendCounter uint64

	receiveMutex   sync.Mutex
	receive        cipher.AEAD
	receiveCounter uint64
}

func newSession(shared []byte, transcript []byte, sendLabel string, receiveLabel string) (*Session, error) {
	send, err := newAEAD(shared, transcript, sendLabel)
	if err != nil {
		return nil, err
	}
	receive, err := newAEAD(shared, transcript, receiveLabel)
	if err != nil {
		return nil, err
	}
	return &Session{send: send, receive: receive}, nil
}

func newAEAD(shared []byte, transcript []byte, label string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, shared, transcript, label, keySize)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(counter uint64) []byte {
	value := make([]byte, 12)
	binary.BigEndian.PutUint64(value[4:], counter)
	return value
}

// Seal appends the encrypted plaintext to dst and returns the result.
func (s *Session) Seal(dst []byte, plaintext []byte) []byte {
	s.sendMutex.Lock()
	defer s.sendMutex.Unlock()
	if s.sendCounter == math.MaxUint64 {
		// Unreachable in practice; reusing a nonce would be far worse.
		panic("e2e: message counter exhausted")
	}
	sealed := s.send.Seal(dst, nonce(s.sendCounter), plaintext, nil)
	s.sendCounter++
	return sealed
}

// Open decrypts the next received message, appending it to dst; dst may be
// ciphertext[:0] to decrypt in place.
func (s *Session) Open(dst []byte, ciphertext []byte) ([]byte, error) {
	s.receiveMutex.Lock()
	defer s.receiveMutex.Unlock()
	opened, err := s.receive.Open(dst, nonce(s.receiveCounter), ciphertext, nil)
	if err != nil {
		return nil, ErrDecryption
	}
	s.receiveCounter++
	return opened, nil
}
