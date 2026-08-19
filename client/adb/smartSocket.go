package adb

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
)

const DefaultAddress = "127.0.0.1:5037"
const responseOkay = "OKAY"
const smartSocketMessageFormat = "%04X%s"

// maxSmartSocketResponseSize bounds a single smartsocket status response
// body (e.g. a device list or an error message), not any ADB stream data —
// stream bytes are relayed verbatim without going through this buffer.
const maxSmartSocketResponseSize = 1024 * 1024

type IAdbSmartSocket interface {
	Connect(targetSerial string) error
	DeviceList() ([]Device, error)
	// Transport returns a connection already inside "transport mode" for
	// targetSerial. Real adb-server does not expose a raw ADB
	// wire-protocol (CNXN/OPEN/WRTE/...) pass-through here: the connection
	// still expects a second smartsocket-style service request next, which
	// is what OpenStream sends. Transport is exposed mainly for tests and
	// callers that want to issue their own service request.
	Transport(targetSerial string) (net.Conn, error)
	// OpenStream selects targetSerial and requests service (e.g.
	// "shell,v2,raw:echo hi", "sync:") on a fresh connection, returning it
	// positioned right after the OKAY/FAIL status, ready for the raw byte
	// stream that service produces/consumes.
	OpenStream(targetSerial string, service string) (net.Conn, error)
}

type AdbSmartSocket struct {
	Address string

	//Dependencies
	logger *slog.Logger
}

func NewAdbSmartSocket(logger *slog.Logger) IAdbSmartSocket {
	return &AdbSmartSocket{
		Address: DefaultAddress,
		logger:  logger,
	}
}

func (ss *AdbSmartSocket) DeviceList() ([]Device, error) {
	body, err := ss.executeCommand("host:devices")
	if err != nil {
		return nil, err
	}
	deviceList := make([]Device, 0)
	var lastIndex = 0
	for lastIndex < len(body) {
		deviceIdLastIndex := lastIndex
		for deviceIdLastIndex < len(body) && body[deviceIdLastIndex] != 0x09 {
			deviceIdLastIndex++
		}
		deviceTypeLastIndex := deviceIdLastIndex + 1
		for deviceTypeLastIndex < len(body) && body[deviceTypeLastIndex] != 0x0a {
			deviceTypeLastIndex++
		}
		deviceId := string(body[lastIndex:deviceIdLastIndex])
		deviceType := string(body[deviceIdLastIndex+1 : deviceTypeLastIndex])
		deviceList = append(deviceList, Device{
			Id:   deviceId,
			Type: deviceType,
		})
		lastIndex = deviceTypeLastIndex + 1
	}
	return deviceList, nil
}

func (ss *AdbSmartSocket) Transport(targetSerial string) (net.Conn, error) {
	conn, err := net.Dial("tcp", ss.Address)
	if err != nil {
		return nil, err
	}
	if err := ss.selectTransport(conn, targetSerial); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (ss *AdbSmartSocket) OpenStream(targetSerial string, service string) (net.Conn, error) {
	conn, err := net.Dial("tcp", ss.Address)
	if err != nil {
		return nil, err
	}
	if err := ss.selectTransport(conn, targetSerial); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := ss.sendCommand(conn, service); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := ss.checkResult(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (ss *AdbSmartSocket) selectTransport(conn net.Conn, targetSerial string) error {
	logger := ss.logger
	command := fmt.Sprintf("host:transport:%s", targetSerial)
	if err := ss.sendCommand(conn, command); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("Selected transport for %s", targetSerial))
	return ss.checkResult(conn)
}

func (ss *AdbSmartSocket) Connect(targetSerial string) error {
	logger := ss.logger
	logger.Info(fmt.Sprintf("Connect called with targetSerial: %s", targetSerial))
	command := fmt.Sprintf("host:connect:%s", targetSerial)
	_, err := ss.executeCommand(command)
	return err
}

func (ss *AdbSmartSocket) sendCommand(conn net.Conn, command string) error {
	_, err := conn.Write([]byte(fmt.Sprintf(smartSocketMessageFormat, len(command), command)))
	return err
}

func (ss *AdbSmartSocket) executeCommand(command string) ([]byte, error) {
	logger := ss.logger
	logger.Info(fmt.Sprintf("Execute command: %s", command))
	conn, err := net.Dial("tcp", ss.Address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := ss.sendCommand(conn, command); err != nil {
		return nil, err
	}
	if err := ss.checkResult(conn); err != nil {
		return nil, err
	}
	return ss.readResponse(conn)
}

func (ss *AdbSmartSocket) checkResult(connection net.Conn) error {
	logger := ss.logger

	statusBuffer := make([]byte, 4)
	if _, err := io.ReadFull(connection, statusBuffer); err != nil {
		return err
	}
	resultString := string(statusBuffer)
	logger.Info(fmt.Sprintf("Check result called: %s", resultString))
	if resultString == responseOkay {
		return nil
	}
	body, err := ss.readResponse(connection)
	if err != nil {
		return err
	}
	return errors.New(string(body))
}

func (ss *AdbSmartSocket) readResponse(connection net.Conn) ([]byte, error) {
	logger := ss.logger
	lengthBuffer := make([]byte, 4)
	if _, err := io.ReadFull(connection, lengthBuffer); err != nil {
		return nil, err
	}
	responseLength, err := strconv.ParseInt(string(lengthBuffer), 16, 0)
	if err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("Response length: %d", responseLength))
	if responseLength > maxSmartSocketResponseSize {
		return nil, fmt.Errorf("response length %d exceeds the maximum allowed size (%d)", responseLength, maxSmartSocketResponseSize)
	}
	body := make([]byte, responseLength)
	if _, err := io.ReadFull(connection, body); err != nil {
		return nil, err
	}
	return body, nil
}
