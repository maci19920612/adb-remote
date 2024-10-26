package transportLayer

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/shared"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"fmt"
	"hash/crc32"
	"log/slog"
	"net"
)

const messageChannelBufferSize = 0

type Client struct {
	connection     *net.Conn
	context        *context.Context
	cancelFunc     *context.CancelFunc
	MessageChannel chan *protocol.TransporterMessage

	//Dependencies
	TransporterMessagePool *shared.TransportMessagePool
	Logger                 *slog.Logger
	Config                 *config.ClientConfiguration
}

func CreateClient(logger *slog.Logger, config *config.ClientConfiguration) (*Client, error) {

	client := &Client{
		MessageChannel: make(chan *protocol.TransporterMessage, messageChannelBufferSize),

		//Dependencies
		TransporterMessagePool: shared.CreateTransporterMessagePool(),
		Logger:                 logger,
		Config:                 config,
	}

	return client, nil
}
func (c *Client) Start() error {
	address := c.Config.TransporterAddress
	connection, err := net.Dial("tcp", address)
	if err != nil {
		return err
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	c.context = &ctx
	c.cancelFunc = &cancelFunc
	c.connection = &connection
	go c.startReader()
	return nil
}

func (c *Client) startReader() {
	log := c.Logger
	mPool := c.TransporterMessagePool
	ctx := *c.context
	for {
		select {
		case <-ctx.Done():
			log.Info("Connection reader cancelled")
			break
		default:
			m := mPool.Obtain()
			err := m.Read(c.connection)
			if err != nil {
				log.Error("%s error happened during reading: ", err)
			}
			c.MessageChannel <- m
		}
	}
}

func (c *Client) Close() {
	conn := *c.connection
	cancelFunc := *c.cancelFunc
	_ = conn.Close()
	cancelFunc()
}

func (c *Client) Release(message *protocol.TransporterMessage) {
	c.TransporterMessagePool.Release(message)
}

func (c *Client) SendError(command uint32, errorCode int, errorMessage string) error {
	log := c.Logger
	mPool := c.TransporterMessagePool
	log.Info(fmt.Sprintf("SendError(%d, %s) called", errorCode, errorMessage))

	errorTransporterMessage := mPool.Obtain()
	errorTransporterMessage.SetErrorResponseCommand(command)
	err := errorTransporterMessage.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	})
	if err != nil {
		return err
	}
	return errorTransporterMessage.Write(c.connection)
}

func (c *Client) SendConnect() error {
	log := c.Logger
	mPool := c.TransporterMessagePool

	log.Info("SendConnect called")

	m := mPool.Obtain()
	defer mPool.Release(m)
	m.SetDirectCommand(protocol.CommandConnect)
	err := m.SetPayloadConnect(&protocol.TransporterMessagePayloadConnect{
		ProtocolVersion: protocol.ProtocolVersion,
	})
	if err != nil {
		return err
	}
	err = m.Write(c.connection)
	return err
}

func (c *Client) SendCreateRoom() error {
	log := c.Logger
	mPool := c.TransporterMessagePool

	log.Info("SendCreateRoom called")
	m := mPool.Obtain()
	defer mPool.Release(m)

	m.SetDirectCommand(protocol.CommandCreateRoom)
	err := m.Write(c.connection)
	return err
}

func (c *Client) SendJoinRoom(roomId string) error {
	log := c.Logger
	mPool := c.TransporterMessagePool
	m := mPool.Obtain()
	defer mPool.Release(m)

	log.Info("SendJoinRoom(%s) called", roomId)

	m.SetDirectCommand(protocol.CommandJoinRoom)
	err := m.SetPayloadConnectRoom(&protocol.TransporterMessagePayloadConnectRoom{
		RoomId: roomId,
	})
	if err != nil {
		return err
	}
	err = m.Write(c.connection)
	return err
}

func (c *Client) SendJoinRoomResponse(isAccepted int) error {
	log := c.Logger
	mPool := c.TransporterMessagePool
	m := mPool.Obtain()
	defer mPool.Release(m)

	log.Info("SendJoinRoomResponse(%d) called", isAccepted)

	m.SetResponseCommand(protocol.CommandJoinRoom)
	err := m.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{
		Accepted: isAccepted,
	})
	if err != nil {
		return err
	}
	err = m.Write(c.connection)
	return err
}

func (c *Client) SendAdbMessage(message *adb.AdbMessage) error {
	var err error
	logger := c.Logger
	mPool := c.TransporterMessagePool
	transportMessage := mPool.Obtain()
	defer mPool.Release(transportMessage)

	length := message.DataLength() + adb.HeaderSize
	checksum := crc32.ChecksumIEEE(message.Data()[:length])

	logger.Info("Sending ADB message to transport")
	transportMessage.SetHeader(protocol.CommandAdbTransport, length, checksum)
	err = transportMessage.WriteHeader(c.connection)
	if err != nil {
		return err
	}
	err = message.Write(c.connection)
	if err != nil {
		return err
	}
	return nil
}
