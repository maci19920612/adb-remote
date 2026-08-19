package transportLayer

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/shared/protocol"
	"adb-remote.maci.team/shared/utils"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

const messageChannelBufferSize = 0

// MessageContainer is the pooled, disposable handle a caller receives for
// every message read off the wire. The caller must call Dispose once done
// reading it so the underlying buffer can be reused.
type MessageContainer = utils.DisposableObjectContainer[protocol.TransporterMessage]

type Client struct {
	connection net.Conn
	cancelFunc context.CancelFunc

	// writeMutex serializes writes to connection. Owner-side stream
	// multiplexing calls SendAdbMessage concurrently from one goroutine
	// per open ADB stream; without this, two concurrent Write calls on the
	// same net.Conn could interleave their bytes on the wire and corrupt
	// the transporter protocol framing.
	writeMutex sync.Mutex

	messageChannel chan *MessageContainer

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

		//Dependencies
		transporterMessagePool: utils.NewObjectPool(factory),
		Logger:                 logger,
		Config:                 config,
	}

	return client, nil
}

// Messages yields every TransporterMessage read off the wire. The receiver
// owns the returned container and must Dispose it once done.
func (c *Client) Messages() <-chan *MessageContainer {
	return c.messageChannel
}

// Start dials the transporter over TLS. The transporter's certificate is
// self-signed (see transporter/tlsutil) with no CA behind it, so this only
// encrypts the connection against passive eavesdropping — it does not
// authenticate the transporter, hence InsecureSkipVerify. Authenticating
// who you're actually talking to end-to-end (the room owner/guest, not the
// relay in between) is what client/identity's public-key fingerprints are
// for, checked at the application layer during room join.
func (c *Client) Start() error {
	address := c.Config.TransporterAddress
	connection, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return err
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	c.cancelFunc = cancelFunc
	c.connection = connection
	go c.startReader(ctx)
	return nil
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
		select {
		case c.messageChannel <- container:
		case <-ctx.Done():
			_ = container.Dispose()
			return
		}
	}
}

// Close terminates the connection and stops the background reader.
func (c *Client) Close() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	if c.connection != nil {
		_ = c.connection.Close()
	}
}

// writeMessage serializes m onto the wire; see the writeMutex field comment
// for why concurrent callers need this.
func (c *Client) writeMessage(m *protocol.TransporterMessage) error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	return m.Write(c.connection)
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
		return c.writeMessage(m)
	})
}

// SendJoinRoom requests to join roomId, presenting publicKey as this
// client's identity (see client/identity) so the room owner can verify a
// fingerprint of it out of band before accepting.
func (c *Client) SendJoinRoom(roomId string, publicKey []byte) error {
	c.Logger.Info(fmt.Sprintf("SendJoinRoom(%s) called", roomId))
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetDirectCommand(protocol.CommandJoinRoom)
		if err := m.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{
			RoomId:    roomId,
			PublicKey: publicKey,
		}); err != nil {
			return err
		}
		return c.writeMessage(m)
	})
}

// SendJoinRoomResponse sends the room owner's accept/decline decision,
// presenting ownerPublicKey as this client's identity (see client/identity)
// so the guest can display a fingerprint of it, symmetric with the owner
// verifying the guest's. The transporter fills in the owner's client id
// before forwarding to the guest.
func (c *Client) SendJoinRoomResponse(isAccepted int, ownerPublicKey []byte) error {
	c.Logger.Info(fmt.Sprintf("SendJoinRoomResponse(%d) called", isAccepted))
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetResponseCommand(protocol.CommandJoinRoom)
		if err := m.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{
			Accepted:  isAccepted,
			PublicKey: ownerPublicKey,
		}); err != nil {
			return err
		}
		return c.writeMessage(m)
	})
}

// SendAdbMessage forwards a raw ADB protocol message to the peer on the
// other side of the room, opaque to everything in between.
func (c *Client) SendAdbMessage(message *adb.AdbMessage) error {
	c.Logger.Info("Sending ADB message to transport")
	return c.withMessage(func(m *protocol.TransporterMessage) error {
		m.SetDirectCommand(protocol.CommandAdbTransport)
		if err := m.SetRawPayload(message.Bytes()); err != nil {
			return err
		}
		return c.writeMessage(m)
	})
}
