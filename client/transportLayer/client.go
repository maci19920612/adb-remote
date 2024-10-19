package transportLayer

import (
	"adb-remote.maci.team/shared"
	"adb-remote.maci.team/shared/protocol"
	"context"
	"log/slog"
	"net"
)

const messageChannelBufferSize = 0

type Client struct {
	connection             *net.Conn
	context                *context.Context
	cancelFunc             *context.CancelFunc
	logger                 *slog.Logger
	transporterMessagePool *shared.TransportMessagePool
	MessageChannel         chan *protocol.TransporterMessage
}

func CreateClient(address string) (*Client, error) {
	connection, err := net.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	ctx, cancelFunc := context.WithCancel(context.Background())

	client := &Client{
		connection:             &connection,
		context:                &ctx,
		cancelFunc:             &cancelFunc,
		logger:                 slog.Default(),
		transporterMessagePool: shared.CreateTransporterMessagePool(),
		MessageChannel:         make(chan *protocol.TransporterMessage, messageChannelBufferSize),
	}

	go client.startReader()

	return client, nil
}

func (c *Client) startReader() {
	log := c.logger
	mPool := c.transporterMessagePool
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
	c.transporterMessagePool.Release(message)
}

func (c *Client) SendConnect() error {
	log := c.logger
	mPool := c.transporterMessagePool

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
	log := c.logger
	mPool := c.transporterMessagePool

	log.Info("SendCreateRoom called")
	m := mPool.Obtain()
	defer mPool.Release(m)

	m.SetDirectCommand(protocol.CommandCreateRoom)
	err := m.Write(c.connection)
	return err
}

func (c *Client) SendJoinRoom(roomId string) error {
	log := c.logger
	mPool := c.transporterMessagePool
	m := mPool.Obtain()
	defer mPool.Release(m)

	log.Info("SendJoinRoom(%s) called", roomId)

	m.SetDirectCommand(protocol.CommandConnectRoom)
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
	log := c.logger
	mPool := c.transporterMessagePool
	m := mPool.Obtain()
	defer mPool.Release(m)

	log.Info("SendJoinRoomResponse(%d) called", isAccepted)

	m.SetDirectCommand(protocol.CommandConnectRoomResult)
	err := m.SetPayloadConnectRoomResult(&protocol.TransporterMessagePayloadConnectRoomResult{
		Accepted: isAccepted,
	})
	if err != nil {
		return err
	}
	err = m.Write(c.connection)
	return err
}
