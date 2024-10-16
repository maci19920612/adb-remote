package protocol

import (
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
}

func (message *TransporterMessage) SetErrorPayload(data *TransporterMessagePayloadError) error {
	offset, err := message.writeInt(0, data.ErrorCode)
	if err != nil {
		return err
	}
	payloadLength, err := message.writeString(offset, data.ErrorMessage)
	if err != nil {
		return err
	}
	message.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

//region Connect payload

type TransporterMessagePayloadConnect struct {
	ProtocolVersion uint32
}

func (message *TransporterMessage) GetPayloadConnect() (*TransporterMessagePayloadConnect, error) {
	_, protocolVersion, err := message.readInt(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnect{
		ProtocolVersion: uint32(protocolVersion),
	}, nil
}

func (message *TransporterMessage) SetPayloadConnect(data *TransporterMessagePayloadConnect) error {
	payloadLength, err := message.writeInt(0, int(data.ProtocolVersion))
	if err != nil {
		return err
	}
	message.updatePayloadMetadata(payloadLength)
	return nil
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
	payloadLength, err := message.writeString(0, data.ClientId)
	if err != nil {
		return err
	}
	message.updatePayloadMetadata(payloadLength)
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
	payloadLength, err := message.writeString(0, data.RoomId)
	if err != nil {
		return err
	}
	message.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

// region Connect to room payload
type TransporterMessagePayloadConnectRoom struct {
	RoomId   string
	ClientId string
}

func (message *TransporterMessage) GetPayloadConnectRoom() (*TransporterMessagePayloadConnectRoom, error) {
	offset, roomId, err := message.readString(0)
	if err != nil {
		return nil, err
	}
	_, clientId, err := message.readString(offset)
	return &TransporterMessagePayloadConnectRoom{
		RoomId:   roomId,
		ClientId: clientId,
	}, nil
}

func (message *TransporterMessage) SetPayloadConnectRoom(data *TransporterMessagePayloadConnectRoom) error {
	offset, err := message.writeString(0, data.RoomId)
	if err != nil {
		return err
	}
	payloadLength, err := message.writeString(offset, data.ClientId)
	message.updatePayloadMetadata(payloadLength)
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
	payloadLength, err := message.writeInt(0, data.Accepted)
	if err != nil {
		return err
	}
	message.updatePayloadMetadata(payloadLength)
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

func (message *TransporterMessage) updatePayloadMetadata(payloadLength uint32) {
	targetBuffer := message.payloadBuffer[:payloadLength]
	ByteOrder.PutUint32(message.payloadLengthBuffer, payloadLength)
	ByteOrder.PutUint32(message.payloadCrc32Buffer, crc32.ChecksumIEEE(targetBuffer))
}

//endregion
