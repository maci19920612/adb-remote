package adb

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
)

const DefaultAddress = "127.0.0.1:5037"
const responseOkay = "OKAY"
const responseFail = "FAIL"
const smartSocketMessageFormat = "%04X%s"

type IAdbSmartSocket interface {
	Connect(targetSerial string) error
	DeviceList() ([]Device, error)
	Transport(targetSerial string) (*net.Conn, error)
}

type AdbSmartSocket struct {
	Address          string
	messageBuffer    []byte
	messageIntBuffer []byte

	//Dependencies
	logger *slog.Logger
}

func NewAdbSmartSocket() IAdbSmartSocket {
	return &AdbSmartSocket{
		Address:          DefaultAddress,
		messageBuffer:    make([]byte, 1024*1024),
		messageIntBuffer: make([]byte, 4),
	}
}

func (ss *AdbSmartSocket) DeviceList() ([]Device, error) {
	length, err := ss.executeCommand("host:devices")
	if err != nil {
		return nil, err
	}
	fmt.Println(hex.Dump(ss.messageBuffer[:length]))
	deviceList := make([]Device, 0)
	var lastIndex = 0
	for lastIndex < length {
		deviceIdLastIndex := lastIndex
		for deviceIdLastIndex < length && ss.messageBuffer[deviceIdLastIndex] != 0x09 {
			deviceIdLastIndex++
		}
		deviceTypeLastIndex := deviceIdLastIndex + 1
		for deviceTypeLastIndex < length && ss.messageBuffer[deviceTypeLastIndex] != 0x0a {
			deviceTypeLastIndex++
		}
		deviceId := string(ss.messageBuffer[lastIndex:deviceIdLastIndex])
		deviceType := string(ss.messageBuffer[deviceIdLastIndex+1 : deviceTypeLastIndex])
		deviceList = append(deviceList, Device{
			Id:   deviceId,
			Type: deviceType,
		})
		lastIndex = deviceTypeLastIndex + 1
	}
	return deviceList, nil
}

func (ss *AdbSmartSocket) Transport(targetSerial string) (*net.Conn, error) {
	logger := ss.logger
	conn, err := net.Dial("tcp", ss.Address)
	if err != nil {
		return nil, err
	}
	command := fmt.Sprintf("host:transport:%s", targetSerial)
	length, err := conn.Write([]byte(fmt.Sprintf(smartSocketMessageFormat, len(command), command)))
	logger.Info(fmt.Sprintf("Write return value: %d", length))
	if err != nil {
		return nil, err
	}
	if err := ss.checkResult(&conn); err != nil {
		return nil, err
	}
	return &conn, nil
}

func (ss *AdbSmartSocket) Connect(targetSerial string) error {
	logger := ss.logger
	logger.Info(fmt.Sprintf("Connect called with targetSerial: %s", targetSerial))
	command := fmt.Sprintf("host:connect:%s", targetSerial)
	_, err := ss.executeCommand(command)
	return err
}

func (ss *AdbSmartSocket) executeCommand(command string) (int, error) {
	logger := ss.logger
	logger.Info(fmt.Sprintf("Execute command: %s", command))
	conn, err := net.Dial("tcp", ss.Address)
	defer conn.Close()
	if err != nil {
		return 0, err
	}
	commandLength := len(command)
	conn.Write([]byte(fmt.Sprintf(smartSocketMessageFormat, commandLength, command)))
	if err := ss.checkResult(&conn); err != nil {
		return 0, err
	}
	length, err := ss.readResponse(&conn)
	if err != nil {
		return 0, err
	}
	return length, nil
}

func (ss *AdbSmartSocket) checkResult(connection *net.Conn) error {
	logger := ss.logger

	length, err := (*connection).Read(ss.messageIntBuffer)
	if err != nil && err != io.EOF {
		return err
	}
	if err := ensureBufferFull(&ss.messageIntBuffer, length); err != nil {
		return err
	}
	resultString := string(ss.messageIntBuffer)
	logger.Info(fmt.Sprintf("Check result called: %s", resultString))
	if resultString == responseOkay {
		return nil
	}
	length, err = ss.readResponse(connection)
	if err != nil {
		return err
	}
	return fmt.Errorf("Error: %s", string(ss.messageBuffer[:length]))
}

func (ss *AdbSmartSocket) readResponse(connection *net.Conn) (int, error) {
	logger := ss.logger
	length, err := (*connection).Read(ss.messageIntBuffer)
	if err != nil && err != io.EOF {
		return 0, err
	}
	if err := ensureBufferFull(&ss.messageIntBuffer, length); err != nil {
		return 0, err
	}
	responseLength, err := strconv.ParseInt(string(ss.messageIntBuffer), 16, 0)
	logger.Info(fmt.Sprintf("Response length: %d", responseLength))
	if err != nil {
		return 0, err
	}
	logger.Info(fmt.Sprintf("Response length: %d", responseLength))
	if int(responseLength) > len(ss.messageBuffer) {
		return 0, errors.New("invalid response length, the target buffer too small to handle the responses")
	}
	responseContainer := ss.messageBuffer[:responseLength]
	length, err = (*connection).Read(responseContainer)
	if err != nil && err != io.EOF {
		return 0, err
	}
	if err := ensureBufferFull(&responseContainer, length); err != nil {
		return 0, err
	}
	return length, nil
}

func ensureBufferFull(self *[]byte, actual int) error {
	if len(*self) != actual {
		return fmt.Errorf("invalid buffer boundary, expected %d, actual: %d", len(*self), actual)
	}
	return nil
}
