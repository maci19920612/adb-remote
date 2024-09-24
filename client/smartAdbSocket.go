package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"

	models "adb-remote.maci.team/client/models"
)

var logger = log.Default()

const DefaultAddress = "127.0.0.1:5037"
const responseOkay = "OKAY"
const responseFail = "FAIL"
const smartSocketMessageFormat = "%04X%s"

type AdbHost struct {
	Address          string
	messageBuffer    []byte
	messageIntBuffer []byte
}

func CreateDefaultHost() *AdbHost {
	return &AdbHost{
		Address:          DefaultAddress,
		messageBuffer:    make([]byte, 1024*1024),
		messageIntBuffer: make([]byte, 4),
	}
}

func (self *AdbHost) executeCommand(command string) (int, error) {
	logger.Println("Execute command: ", command)
	conn, err := net.Dial("tcp", self.Address)
	defer conn.Close()
	if err != nil {
		return 0, err
	}
	commandLength := len(command)
	conn.Write([]byte(fmt.Sprintf(smartSocketMessageFormat, commandLength, command)))
	if err := self.checkResult(&conn); err != nil {
		return 0, err
	}
	length, err := self.readResponse(&conn)
	if err != nil {
		return 0, err
	}
	return length, nil
}

func (self *AdbHost) DeviceList() ([]models.Device, error) {
	result, err := self.executeCommand("host:devices")
	if err != nil {
		return nil, err
	}
	fmt.Println(hex.Dump(result))
	deviceList := make([]models.Device, 0)
	var lastIndex = 0
	var resultLength = len(result)
	for lastIndex < resultLength {
		deviceIdLastIndex := lastIndex
		for deviceIdLastIndex < resultLength && result[deviceIdLastIndex] != 0x09 {
			deviceIdLastIndex++
		}
		deviceTypeLastIndex := deviceIdLastIndex + 1
		for deviceTypeLastIndex < resultLength && result[deviceTypeLastIndex] != 0x0a {
			deviceTypeLastIndex++
		}
		deviceId := string(result[lastIndex:deviceIdLastIndex])
		deviceType := string(result[deviceIdLastIndex+1 : deviceTypeLastIndex])
		deviceList = append(deviceList, models.Device{
			Id:   deviceId,
			Type: deviceType,
		})
		lastIndex = deviceTypeLastIndex + 1
	}
	return deviceList, nil
}

func (self *AdbHost) Transport(targetSerial string) (*net.Conn, error) {
	conn, err := net.Dial("tcp", self.Address)
	if err != nil {
		return nil, err
	}
	command := fmt.Sprintf("host:transport:%s", targetSerial)
	conn.Write([]byte(fmt.Sprintf(smartSocketMessageFormat, len(command), command)))
}

func (self *AdbHost) checkResult(connection *net.Conn) error {
	length, err := (*connection).Read(self.messageIntBuffer)
	if err != nil && err != io.EOF {
		return err
	}
	if err := ensureBufferFull(&self.messageIntBuffer, length); err != nil {
		return err
	}
	resultString := string(self.messageIntBuffer)
	logger.Println("Check result called: ", resultString)
	if resultString == responseOkay {
		return nil
	}
	length, err = self.readResponse(connection)
	if err != nil {
		return err
	}
	return fmt.Errorf("Error: %s", string(self.messageBuffer[:length]))
}

func (self *AdbHost) readResponse(connection *net.Conn) (int, error) {
	length, err := (*connection).Read(self.messageIntBuffer)
	if err != nil && err != io.EOF {
		return 0, err
	}
	ensureBufferFull(&self.messageIntBuffer, length)
	length, err = hex.Decode(self.messageBuffer[4:], self.messageBuffer[:4])
	if err != nil {
		return 0, err
	}
	responseLength := binary.LittleEndian.Uint32(self.messageBuffer[4:8])
	if int(responseLength) > len(self.messageBuffer) {
		return 0, errors.New("Invalid response length, the target buffer too small to handle the responses")
	}
	responseContainer := self.messageBuffer[:responseLength]
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
		return fmt.Errorf("Invalid buffer boundary, expected %d, actual: %d", len(*self), actual)
	}
	return nil
}
