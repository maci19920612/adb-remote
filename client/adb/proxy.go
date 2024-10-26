package adb

import (
	"adb-remote.maci.team/client/transportLayer"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
)

type IAdbProxy interface {
	Start(roomId string) error
	Stop()
	Write(message *AdbMessage) error
}

type AdbProxy struct {
	port       string
	ctx        *context.Context
	cancelFunc *context.CancelFunc
	listener   *net.Listener
	conn       *net.Conn
	adbMessage *AdbMessage
	//Dependencies
	logger *slog.Logger
	client *transportLayer.Client
}

func NewAdbProxy(port string, logger *slog.Logger, client *transportLayer.Client) IAdbProxy {
	ctx, cancelFunc := context.WithCancel(context.Background())
	return &AdbProxy{
		port:       port,
		ctx:        &ctx,
		cancelFunc: &cancelFunc,
		adbMessage: CreateMessage(),

		//Dependencies
		logger: logger,
		client: client,
	}
}

func (p *AdbProxy) Start(roomId string) error {
	logger := p.logger
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%s", p.port))
	if err != nil {
		return err
	}
	p.listener = &listener
	//TODO: Move this into it's separate method
	go func() {
	mainLoop:
		for {
			select {
			case <-(*p.ctx).Done():
				logger.Info("AdbProxy stopped, exiting from the connection ACCEPT loop")
				break mainLoop
			default:
				conn, err := listener.Accept()
				if err != nil {
					var opErr *net.OpError
					if errors.As(err, &opErr) && opErr.Op == "accept" {
						logger.Info("The listener closed")
					} else {
						logger.Error(fmt.Sprintf("Error during the connection ACCEPT %s", err))
					}
					break mainLoop
				}
				logger.Info("Starting the new connection")
				p.conn = &conn
			connectionLoop:
				for {
					select {
					case <-(*p.ctx).Done():
						logger.Info("AdbProxy stopped, exiting the connection READER loop")
						break mainLoop
					default:
						err = p.adbMessage.Read(p.conn)
						if err != nil {
							logger.Error(fmt.Sprintf("Invalid ADB message read from the network: %s", err))
							_ = conn.Close()
							p.conn = nil
							break connectionLoop
						}
						if command := p.adbMessage.Command(); command != CommandConnect {
							logger.Info(fmt.Sprintf("Unexpected command from the local ADB instance: %x", command))
							_ = conn.Close()
							p.conn = nil
							break connectionLoop
						}
						protocolVersion := p.adbMessage.Arg1()
						maxMessageSize := p.adbMessage.Arg2()
						logger.Info(fmt.Sprintf("Protocol version: %d, max message size: %d", protocolVersion, maxMessageSize))
						if maxMessageSize > MaxPayloadLength {
							logger.Error(fmt.Sprintf("The local adb message size is too low, max allowed message size: %d", MaxPayloadLength))
							_ = conn.Close()
							p.conn = nil
							break connectionLoop
						}
						p.adbMessage.Set(CommandConnect, protocolVersion, maxMessageSize, []byte(fmt.Sprintf("device:wrapper-remote-%s", roomId)))
						err = p.adbMessage.Write(&conn)
						if err != nil {
							logger.Error(fmt.Sprintf("Error during the CNXN adb response sending: %s", err))
							_ = conn.Close()
							p.conn = nil
							break connectionLoop
						}
						for {
							err = p.adbMessage.Read(&conn)
							if err != nil {
								logger.Error(fmt.Sprintf("Error during the ADB message reading: %s", err))
								_ = conn.Close()
								p.conn = nil
								break connectionLoop
							}
							p.client.SendCreateRoom()
						}
					}
				}
			}
		}
	}()
	return nil
}

func (p *AdbProxy) Stop() {
	if p.conn != nil {
		_ = (*p.conn).Close()
		p.conn = nil
	}
	if p.listener != nil {
		_ = (*p.listener).Close()
		p.listener = nil
	}
	(*p.cancelFunc)()
	p.ctx = nil
	p.cancelFunc = nil
	p.conn = nil
	p.listener = nil
}

func (p *AdbProxy) Write(message *AdbMessage) error {
	logger := p.logger
	if p.conn == nil {
		logger.Error("No active connection with the local ADB instance, invalid WRITE attempt")
		return errors.New("no active connection with the local ADB instance")
	}
	err := message.Write(p.conn)
	if err != nil {
		logger.Error(fmt.Sprintf("Error during the ADB message writing: %s", err))
		_ = (*p.conn).Close()
		p.conn = nil
		return err
	}
	return nil
}
