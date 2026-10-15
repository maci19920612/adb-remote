package transportLayer

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/e2e"
	"adb-remote.maci.team/client/pcapwriter"
	"adb-remote.maci.team/shared/certfingerprint"
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/shared/utils"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const messageChannelBufferSize = 0

// MessageContainer is the pooled, disposable handle a caller receives for
// every message read off the wire. The caller must call Dispose once done
// reading it so the underlying buffer can be reused.
type MessageContainer = utils.DisposableObjectContainer[protocol.TransporterMessage]

// ErrNoSession refuses to send ADB traffic before a session with the peer
// exists (see SetSession): it never goes out unencrypted.
var ErrNoSession = errors.New("no end-to-end session with the peer")

type Client struct {
	connection net.Conn
	cancelFunc context.CancelFunc

	// writeMutex serializes writes to connection. Owner-side stream
	// multiplexing calls SendAdbMessage concurrently from one goroutine
	// per open ADB stream; without this, two concurrent Write calls on the
	// same net.Conn could interleave their bytes on the wire and corrupt
	// the transporter protocol framing. It also keeps ADB messages going
	// out in the order they were sealed, which the peer opens them in.
	writeMutex sync.Mutex
	// sealBuffer holds an ADB message while it is encrypted; guarded by
	// writeMutex.
	sealBuffer []byte

	// session encrypts ADB traffic to the peer and decrypts it from them;
	// nil until a guest was accepted (see SetSession).
	session atomic.Pointer[e2e.Session]

	messageChannel chan *MessageContainer

	// bytesSent/bytesReceived count total wire bytes (header+payload) across
	// every message, for the TUI's transfer-stats footer. Accessed
	// concurrently from writeMessage (any sender goroutine) and startReader
	// (the one reader goroutine).
	bytesSent     atomic.Uint64
	bytesReceived atomic.Uint64

	// capture is non-nil only when debug packet capture is enabled (see
	// EnableDebugCapture); nil-checked on every send/receive so the common
	// case stays a single atomic load.
	capture atomic.Pointer[packetCapture]

	//Dependencies
	transporterMessagePool *utils.ObjectPool[protocol.TransporterMessage]
	Logger                 *slog.Logger
	Config                 *config.ClientConfiguration
}

func CreateClient(logger *slog.Logger, config *config.ClientConfiguration) (*Client, error) {
	factory := func() *protocol.TransporterMessage {
		return protocol.CreateTransporterMessage()
	}
	client := &Client{
		messageChannel: make(chan *MessageContainer, messageChannelBufferSize),
		sealBuffer:     make([]byte, 0, adb.HeaderSize+adb.MaxPayloadLength+e2e.Overhead),

		//Dependencies
		transporterMessagePool: utils.NewObjectPool(factory),
		Logger:                 logger,
		Config:                 config,
	}

	return client, nil
}

// Messages yields every TransporterMessage read off the wire, with
// CommandAdbTransport payloads already decrypted. The receiver owns the
// returned container and must Dispose it once done.
func (c *Client) Messages() <-chan *MessageContainer {
	return c.messageChannel
}

// SetSession installs the end-to-end session with the room's other side
// (see client/e2e), or removes it when session is nil. ADB traffic only
// flows while one is installed: SendAdbMessage refuses to send without
// one, and incoming ADB traffic is dropped without one.
func (c *Client) SetSession(session *e2e.Session) {
	c.session.Store(session)
}

// packetCapture bundles a pcap writer with the file it owns, so Close can
// flush/close it.
type packetCapture struct {
	writer *pcapwriter.Writer
	file   *os.File
}

// EnableDebugCapture starts recording every message this client sends and
// receives to a pcap file at path (see client/pcapwriter for what "capture"
// means here — the plaintext protocol traffic, not real packets off a
// NIC). Meant for debug-verbosity logging; safe to call before or after
// Start().
func (c *Client) EnableDebugCapture(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create the packet capture file: %w", err)
	}
	writer, err := pcapwriter.NewWriter(file)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("failed to write the packet capture header: %w", err)
	}
	c.capture.Store(&packetCapture{writer: writer, file: file})
	return nil
}

// recordCapture is a no-op unless EnableDebugCapture has been called.
// message is recorded as the client sees it, i.e. with an ADB transport
// payload in plaintext: payload, when not nil, replaces the (encrypted)
// payload on the wire.
func (c *Client) recordCapture(direction pcapwriter.Direction, message *protocol.TransporterMessage, payload []byte) {
	capture := c.capture.Load()
	if capture == nil {
		return
	}
	data := message.Bytes()
	if payload != nil {
		// Debug capture only, so a fresh buffer per message is fine.
		plaintext := protocol.CreateTransporterMessage()
		plaintext.SetDirectCommand(message.Command())
		if err := plaintext.SetRawPayload(payload); err != nil {
			c.Logger.Error(fmt.Sprintf("Failed to build a packet capture record: %s", err))
			return
		}
		data = plaintext.Bytes()
	}
	if err := capture.writer.WritePacket(direction, data, time.Now()); err != nil {
		c.Logger.Error(fmt.Sprintf("Failed to write a packet capture record: %s", err))
	}
}

// Start dials the transporter over TLS 1.3. The transporter's certificate
// is self-signed (see transporter/tlsutil) with no CA behind it, so there
// is no chain to verify (hence InsecureSkipVerify). Unless the
// configuration pins the certificate's fingerprint, that only encrypts the
// connection against passive eavesdropping and authenticates nothing; with
// a pinned fingerprint, the handshake fails for any other certificate. The
// ADB traffic doesn't rely on either: that is encrypted end to end between
// the room's owner and guest (see client/e2e), under keys bound to the
// identities whose fingerprints the two compare out of band.
func (c *Client) Start() error {
	address := c.Config.TransporterAddress
	tlsConfig, err := transporterTLSConfig(c.Config.TransporterCertificateFingerprint)
	if err != nil {
		return err
	}
	connection, err := tls.Dial("tcp", address, tlsConfig)
	if err != nil {
		return err
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	c.cancelFunc = cancelFunc
	c.connection = connection
	go c.startReader(ctx)
	return nil
}

// transporterTLSConfig returns the TLS configuration for dialing the
// transporter, accepting only the certificate with pinnedFingerprint when
// that isn't empty.
func transporterTLSConfig(pinnedFingerprint string) (*tls.Config, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
	if pinnedFingerprint == "" {
		return tlsConfig, nil
	}
	pinned, err := certfingerprint.Parse(pinnedFingerprint)
	if err != nil {
		return nil, fmt.Errorf("invalid transporterCertificateFingerprint: %w", err)
	}
	// Called even with InsecureSkipVerify; rawCerts[0] is the certificate
	// the transporter presented.
	tlsConfig.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("the transporter presented no certificate")
		}
		if certfingerprint.Digest(rawCerts[0]) != pinned {
			return fmt.Errorf("the transporter's TLS certificate %s doesn't match the pinned transporterCertificateFingerprint %s", certfingerprint.Of(rawCerts[0]), strings.TrimSpace(pinnedFingerprint))
		}
		return nil
	}
	return tlsConfig, nil
}

// startReader is the sole sender on messageChannel, so it alone is
// responsible for closing it once reading stops for any reason (read error,
// a broken pool, or ctx cancellation) — this is how Messages() consumers
// learn the connection is gone, rather than blocking forever.
func (c *Client) startReader(ctx context.Context) {
	log := c.Logger
	pool := c.transporterMessagePool
	defer close(c.messageChannel)
	for {
		if ctx.Err() != nil {
			log.Info("Connection reader cancelled")
			return
		}
		container := pool.Obtain()
		message, err := container.Data()
		if err != nil {
			// The container was handed back to us in a disposed state,
			// which can only happen if the pool itself is broken.
			log.Error(fmt.Sprintf("Unusable pooled message container: %s", err))
			_ = container.Dispose()
			return
		}
		if err := message.Read(c.connection); err != nil {
			log.Error(fmt.Sprintf("Error happened during reading: %s", err))
			_ = container.Dispose()
			return
		}
		c.bytesReceived.Add(uint64(message.WireSize()))
		if message.Command() == protocol.CommandAdbTransport {
			deliver, err := c.openAdbTransport(message)
			if err != nil {
				// Something between us and the peer altered, dropped or
				// injected traffic; nothing that follows can be trusted.
				log.Error(fmt.Sprintf("Closing the connection: %s", err))
				_ = container.Dispose()
				_ = c.connection.Close()
				return
			}
			if !deliver {
				_ = container.Dispose()
				continue
			}
		} else {
			c.recordCapture(pcapwriter.Incoming, message, nil)
		}
		select {
		case c.messageChannel <- container:
		case <-ctx.Done():
			_ = container.Dispose()
			return
		}
	}
}

// openAdbTransport decrypts an incoming ADB message in place. It reports
// false for a message to drop, one that arrived without a session.
func (c *Client) openAdbTransport(message *protocol.TransporterMessage) (bool, error) {
	session := c.session.Load()
	if session == nil {
		c.Logger.Warn("Dropping an ADB message that arrived without an end-to-end session")
		return false, nil
	}
	plaintext, err := session.Open(message.Payload()[:0], message.Payload())
	if err != nil {
		return false, err
	}
	if err := message.SetRawPayload(plaintext); err != nil {
		return false, err
	}
	c.recordCapture(pcapwriter.Incoming, message, nil)
	return true, nil
}

// Close terminates the connection and stops the background reader.
func (c *Client) Close() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	if c.connection != nil {
		_ = c.connection.Close()
	}
	if capture := c.capture.Load(); capture != nil {
		_ = capture.file.Close()
	}
}

// writeMessage serializes m onto the wire; see the writeMutex field comment
// for why concurrent callers need this.
func (c *Client) writeMessage(m *protocol.TransporterMessage) error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	return c.writeLocked(m, nil)
}

// writeLocked writes m in one go; capturePayload, when not nil, is what the
// capture records as its payload (see recordCapture). The caller holds
// writeMutex.
func (c *Client) writeLocked(m *protocol.TransporterMessage, capturePayload []byte) error {
	if _, err := c.connection.Write(m.Bytes()); err != nil {
		return err
	}
	c.bytesSent.Add(uint64(m.WireSize()))
	c.recordCapture(pcapwriter.Outgoing, m, capturePayload)
	return nil
}

// BytesSent and BytesReceived report the total wire bytes (header+payload,
// across every message) sent and received on this connection so far, for
// display (e.g. the TUI's transfer-stats footer). Safe for concurrent use.
func (c *Client) BytesSent() uint64 {
	return c.bytesSent.Load()
}

func (c *Client) BytesReceived() uint64 {
	return c.bytesReceived.Load()
}

// withMessage obtains a pooled message, hands it to fn, and disposes of it
// once fn returns.
func (c *Client) withMessage(fn func(*protocol.TransporterMessage) error) error {
	container := c.transporterMessagePool.Obtain()
	defer container.Dispose()
	message, err := container.Data()
	if err != nil {
		return err
	}
	return fn(message)
}

func (c *Client) SendError(command uint32, errorCode int, errorMessage string) error {
	c.Logger.Info(fmt.Sprintf("SendError(%d, %s) called", errorCode, errorMessage))
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetErrorResponseCommand(command)
		if err := m.SetErrorPayload(&protocol.TransporterMessagePayloadError{
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		}); err != nil {
			return err
		}
		return c.writeMessage(m)
	})
}

func (c *Client) SendConnect() error {
	c.Logger.Info("SendConnect called")
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetDirectCommand(protocol.CommandConnect)
		if err := m.SetPayloadConnect(&protocol.TransporterMessagePayloadConnect{
			ProtocolVersion: protocol.ProtocolVersion,
		}); err != nil {
			return err
		}
		return c.writeMessage(m)
	})
}

func (c *Client) SendCreateRoom() error {
	c.Logger.Info("SendCreateRoom called")
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetDirectCommand(protocol.CommandCreateRoom)
		m.ClearPayload()
		return c.writeMessage(m)
	})
}

// SendJoinRoom requests to join roomId, presenting publicKey as this
// client's identity (see client/identity) so the room owner can verify a
// fingerprint of it out of band before accepting, and offer as this
// client's half of the end-to-end key exchange (see client/e2e).
func (c *Client) SendJoinRoom(roomId string, publicKey []byte, offer e2e.KeyExchange) error {
	c.Logger.Info(fmt.Sprintf("SendJoinRoom(%s) called", roomId))
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetDirectCommand(protocol.CommandJoinRoom)
		if err := m.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{
			RoomId:       roomId,
			PublicKey:    publicKey,
			EphemeralKey: offer.EphemeralKey,
			Signature:    offer.Signature,
		}); err != nil {
			return err
		}
		return c.writeMessage(m)
	})
}

// SendJoinRoomResponse sends the room owner's accept/decline decision about
// guestClientId, presenting ownerPublicKey as this client's identity (see
// client/identity) so the guest can display a fingerprint of it, symmetric
// with the owner verifying the guest's, and answer as this client's half
// of the end-to-end key exchange (empty for a decline). The transporter
// replaces the guest's client id with the owner's before forwarding.
func (c *Client) SendJoinRoomResponse(guestClientId string, isAccepted int, ownerPublicKey []byte, answer e2e.KeyExchange) error {
	c.Logger.Info(fmt.Sprintf("SendJoinRoomResponse(%s, %d) called", guestClientId, isAccepted))
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetResponseCommand(protocol.CommandJoinRoom)
		if err := m.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{
			Accepted:     isAccepted,
			ClientId:     guestClientId,
			PublicKey:    ownerPublicKey,
			EphemeralKey: answer.EphemeralKey,
			Signature:    answer.Signature,
		}); err != nil {
			return err
		}
		return c.writeMessage(m)
	})
}

// SendAdbMessage forwards a raw ADB protocol message to the peer on the
// other side of the room, encrypted end to end so it stays opaque to
// everything in between. It fails with ErrNoSession while there is no
// session with the peer.
func (c *Client) SendAdbMessage(message *adb.AdbMessage) error {
	c.Logger.Info("Sending ADB message to transport")
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		c.writeMutex.Lock()
		defer c.writeMutex.Unlock()
		session := c.session.Load()
		if session == nil {
			return ErrNoSession
		}
		// Sealed under writeMutex, so the messages go out in the order
		// their nonces were taken.
		c.sealBuffer = session.Seal(c.sealBuffer[:0], message.Bytes())
		m.SetDirectCommand(protocol.CommandAdbTransport)
		if err := m.SetRawPayload(c.sealBuffer); err != nil {
			return err
		}
		return c.writeLocked(m, message.Bytes())
	})
}
