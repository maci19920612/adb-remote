package protocol

import (
	"adb-remote.maci.team/shared/utils"
	"fmt"
	"hash/crc32"
)

// region Error payload
type TransporterMessagePayloadError struct {
	ErrorCode    int
	ErrorMessage string
}

func (message *TransporterMessage) GetErrorPayload() (*TransporterMessagePayloadError, error) {
	offset, errorCode, err := message.readInt(0)
	if err != nil {
		return nil, err
	}
	_, errorMessage, err := message.readString(offset)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadError{
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	}, nil
	errorCode := ByteOrder.Uint32(message.payloadBuffer[:4])
	messageLength := ByteOrder.Uint32(message.payloadBuffer[4:8])
	errorMessage := string(message.payloadBuffer[8 : 8+messageLength])
	data := new(TransporterMessagePayloadError)
	data.ErrorCode = errorCode
	data.ErrorMessage = errorMessage
	return data
}

func (message *TransporterMessage) SetErrorPayload(data *TransporterMessagePayloadError) {
	targetBuffer := message.payloadBuffer[:4]
	ByteOrder.PutUint32(targetBuffer, data.ErrorCode)
	errorMessage := []byte(data.ErrorMessage)
	ByteOrder.PutUint32(targetBuffer[4:8], uint32(len(errorMessage)))
	copy(targetBuffer[8:], errorMessage)
	payloadLength := 8 + len(errorMessage)
	ByteOrder.PutUint32(message.payloadCrc32Buffer, crc32.ChecksumIEEE(message.payloadBuffer[:payloadLength]))
	ByteOrder.PutUint32(message.payloadLengthBuffer, uint32(payloadLength))
}

//endregion

//region Connect payload

type TransporterMessagePayloadConnect struct {
	ProtocolVersion uint32
}

func (message *TransporterMessage) GetPayloadConnect() (*TransporterMessagePayloadConnect, error) {
	payloadLength := message.PayloadLength()
	if err := utils.EnsureIntLength(int(payloadLength)); err != nil {
		return nil, err
	}
	data := new(TransporterMessagePayloadConnect)
	data.ProtocolVersion = ByteOrder.Uint32(message.payloadBuffer[:4])
	return data, nil
}

func (message *TransporterMessage) SetPayloadConnect(data *TransporterMessagePayloadConnect) {
	targetBuffer := message.payloadBuffer[:4]
	ByteOrder.PutUint32(targetBuffer, data.ProtocolVersion)
	ByteOrder.PutUint32(message.payloadCrc32Buffer, crc32.ChecksumIEEE(targetBuffer))
	ByteOrder.PutUint32(message.payloadLengthBuffer, uint32(len(targetBuffer)))
}

//endregion

// region Connect response payload
type TransporterMessagePayloadConnectResponse struct {
	ClientId string
}

func (message *TransporterMessage) GetPayloadConnectResponse() (*TransporterMessagePayloadConnectResponse, error) {
	_, clientId, err := message.readString(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnectResponse{
		ClientId: clientId,
	}, nil
}
func (message *TransporterMessage) SetPayloadConnectResponse(data *TransporterMessagePayloadConnectResponse) error {
	_, err := message.writeString(0, data.ClientId)
	if err != nil {
		return err
	}
	return nil
}

//endregion

// region Create room response
type TransporterMessagePayloadCreateRoomResponse struct {
	RoomId string
}

func (message *TransporterMessage) GetPayloadCreateRoomResponse() (*TransporterMessagePayloadCreateRoomResponse, error) {
	_, roomId, err := message.readString(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadCreateRoomResponse{
		RoomId: roomId,
	}, nil
}

func (message *TransporterMessage) SetPayloadCreateRoomResponse(data *TransporterMessagePayloadCreateRoomResponse) error {
	_, err := message.writeString(0, data.RoomId)
	if err != nil {
		return err
	}
	return nil
}

//endregion

// region Connect to room payload
type TransporterMessagePayloadConnectRoom struct {
	RoomId string
}

func (message *TransporterMessage) GetPayloadConnectRoom() (*TransporterMessagePayloadConnectRoom, error) {
	_, roomId, err := message.readString(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnectRoom{
		RoomId: roomId,
	}, nil
}

func (message *TransporterMessage) SetPayloadConnectRoom(data *TransporterMessagePayloadConnectRoom) error {
	_, err := message.writeString(0, data.RoomId)
	if err != nil {
		return err
	}
	return nil
}

//endregion

// region Connect to room response
type TransporterMessagePayloadConnectRoomResult struct {
	Accepted int //0 = false, anything else true
}

func (message *TransporterMessage) GetPayloadConnectRoomResult() (*TransporterMessagePayloadConnectRoomResult, error) {
	_, accepted, err := message.readInt(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnectRoomResult{
		Accepted: accepted,
	}, nil
}

func (message *TransporterMessage) SetPayloadConnectRoomResult(data *TransporterMessagePayloadConnectRoomResult) error {
	_, err := message.writeInt(0, data.Accepted)
	if err != nil {
		return err
	}
	return nil
}

//endregion

// region Util functions
func (message *TransporterMessage) writeInt(offset uint32, value int) (uint32, error) {
	typeSize := uint32(4)
	newOffset := offset + typeSize
	if uint32(len(message.payloadBuffer)) < newOffset {
		return 0, fmt.Errorf("not enough space in the payload buffer, size: %d, offset: %d", len(message.payloadBuffer), newOffset)
	}
	ByteOrder.PutUint32(message.payloadBuffer[offset:typeSize], uint32(value))
	return newOffset, nil
}

func (message *TransporterMessage) writeString(offset uint32, value string) (uint32, error) {
	lengthTypeSize := uint32(4)
	valueBytes := []byte(value)
	newOffset := offset + lengthTypeSize + uint32(len(valueBytes))
	if uint32(len(message.payloadBuffer)) < newOffset {
		return 0, fmt.Errorf("not enough space in the payload buffer, size: %d, offset: %d", len(message.payloadBuffer), newOffset)
	}
	ByteOrder.PutUint32(message.payloadBuffer[offset:lengthTypeSize], uint32(len(valueBytes)))
	copy(message.payloadBuffer[offset+lengthTypeSize:], valueBytes)
	return newOffset, nil
}

func (message *TransporterMessage) readInt(offset uint32) (uint32, int, error) {
	typeSize := uint32(4)
	if uint32(len(message.payloadBuffer)) < typeSize+offset {
		return 0, 0, fmt.Errorf("not enough data in the payload buffer, size: %d, offset: %d", len(message.payloadBuffer), typeSize+offset)
	}
	value := ByteOrder.Uint32(message.payloadBuffer[offset:typeSize])
	return offset + typeSize, int(value), nil
}

func (message *TransporterMessage) readString(offset uint32) (uint32, string, error) {
	lengthTypeSize := uint32(4)
	newOffset := offset + lengthTypeSize
	if uint32(len(message.payloadBuffer)) < newOffset {
		return 0, "", fmt.Errorf("not enough data in the payload buffer, size: %d, offset: %d", len(message.payloadBuffer), newOffset)
	}
	length := ByteOrder.Uint32(message.payloadBuffer[offset:lengthTypeSize])
	newOffset += length
	if uint32(len(message.payloadBuffer)) < newOffset {
		return 0, "", fmt.Errorf("not enough data in the payload buffer, size: %d, offset: %d", len(message.payloadBuffer), newOffset)
	}
	offset += lengthTypeSize
	value := string(message.payloadBuffer[offset : offset+length])
	return newOffset, value, nil
}

//endregion
