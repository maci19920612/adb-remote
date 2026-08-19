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

func (m *TransporterMessage) GetErrorPayload() (*TransporterMessagePayloadError, error) {
	offset, errorCode, err := m.readInt(0)
	if err != nil {
		return nil, err
	}
	_, errorMessage, err := m.readString(offset)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadError{
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	}, nil
}

func (m *TransporterMessage) SetErrorPayload(data *TransporterMessagePayloadError) error {
	offset, err := m.writeInt(0, data.ErrorCode)
	if err != nil {
		return err
	}
	payloadLength, err := m.writeString(offset, data.ErrorMessage)
	if err != nil {
		return err
	}
	m.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

//region Connect payload

type TransporterMessagePayloadConnect struct {
	ProtocolVersion uint32
}

func (m *TransporterMessage) GetPayloadConnect() (*TransporterMessagePayloadConnect, error) {
	_, protocolVersion, err := m.readInt(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnect{
		ProtocolVersion: uint32(protocolVersion),
	}, nil
}

func (m *TransporterMessage) SetPayloadConnect(data *TransporterMessagePayloadConnect) error {
	payloadLength, err := m.writeInt(0, int(data.ProtocolVersion))
	if err != nil {
		return err
	}
	m.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

// region Connect response payload
type TransporterMessagePayloadConnectResponse struct {
	ClientId string
}

func (m *TransporterMessage) GetPayloadConnectResponse() (*TransporterMessagePayloadConnectResponse, error) {
	_, clientId, err := m.readString(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnectResponse{
		ClientId: clientId,
	}, nil
}
func (m *TransporterMessage) SetPayloadConnectResponse(data *TransporterMessagePayloadConnectResponse) error {
	payloadLength, err := m.writeString(0, data.ClientId)
	if err != nil {
		return err
	}
	m.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

// region Create room response
type TransporterMessagePayloadCreateRoomResponse struct {
	RoomId string
}

func (m *TransporterMessage) GetPayloadCreateRoomResponse() (*TransporterMessagePayloadCreateRoomResponse, error) {
	_, roomId, err := m.readString(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadCreateRoomResponse{
		RoomId: roomId,
	}, nil
}

func (m *TransporterMessage) SetPayloadCreateRoomResponse(data *TransporterMessagePayloadCreateRoomResponse) error {
	payloadLength, err := m.writeString(0, data.RoomId)
	if err != nil {
		return err
	}
	m.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

// region Connect to room payload
type TransporterMessagePayloadConnectRoom struct {
	RoomId   string
	ClientId string
}

func (m *TransporterMessage) GetPayloadConnectRoom() (*TransporterMessagePayloadConnectRoom, error) {
	offset, roomId, err := m.readString(0)
	if err != nil {
		return nil, err
	}
	_, clientId, err := m.readString(offset)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnectRoom{
		RoomId:   roomId,
		ClientId: clientId,
	}, nil
}

func (m *TransporterMessage) SetPayloadConnectRoom(data *TransporterMessagePayloadConnectRoom) error {
	offset, err := m.writeString(0, data.RoomId)
	if err != nil {
		return err
	}
	payloadLength, err := m.writeString(offset, data.ClientId)
	if err != nil {
		return err
	}
	m.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

// region Connect to room response
type TransporterMessagePayloadConnectRoomResult struct {
	Accepted int //0 = false, anything else true
}

func (m *TransporterMessage) GetPayloadConnectRoomResponse() (*TransporterMessagePayloadConnectRoomResult, error) {
	_, accepted, err := m.readInt(0)
	if err != nil {
		return nil, err
	}
	return &TransporterMessagePayloadConnectRoomResult{
		Accepted: accepted,
	}, nil
}

func (m *TransporterMessage) SetPayloadConnectRoomResult(data *TransporterMessagePayloadConnectRoomResult) error {
	payloadLength, err := m.writeInt(0, data.Accepted)
	if err != nil {
		return err
	}
	m.updatePayloadMetadata(payloadLength)
	return nil
}

//endregion

// region Raw payload

// SetRawPayload copies an already-encoded, opaque byte slice into the
// message's payload buffer, bypassing the typed payload accessors. Used to
// forward or embed data (e.g. an ADB protocol message) whose framing is
// owned by another layer.
func (m *TransporterMessage) SetRawPayload(data []byte) error {
	if uint32(len(data)) > uint32(len(m.payloadBuffer)) {
		return fmt.Errorf("not enough space in the payload buffer, size: %d, data length: %d", len(m.payloadBuffer), len(data))
	}
	copy(m.payloadBuffer, data)
	m.updatePayloadMetadata(uint32(len(data)))
	return nil
}

//endregion

// region Util functions
func (m *TransporterMessage) writeInt(offset uint32, value int) (uint32, error) {
	typeSize := uint32(4)
	newOffset := offset + typeSize
	if uint32(len(m.payloadBuffer)) < newOffset {
		return 0, fmt.Errorf("not enough space in the payload buffer, size: %d, offset: %d", len(m.payloadBuffer), newOffset)
	}
	ByteOrder.PutUint32(m.payloadBuffer[offset:newOffset], uint32(value))
	return newOffset, nil
}

func (m *TransporterMessage) writeString(offset uint32, value string) (uint32, error) {
	lengthTypeSize := uint32(4)
	valueBytes := []byte(value)
	dataOffset := offset + lengthTypeSize
	newOffset := dataOffset + uint32(len(valueBytes))
	if uint32(len(m.payloadBuffer)) < newOffset {
		return 0, fmt.Errorf("not enough space in the payload buffer, size: %d, offset: %d", len(m.payloadBuffer), newOffset)
	}
	ByteOrder.PutUint32(m.payloadBuffer[offset:dataOffset], uint32(len(valueBytes)))
	copy(m.payloadBuffer[dataOffset:newOffset], valueBytes)
	return newOffset, nil
}

// readInt and readString bound-check against the message's declared
// PayloadLength (the extent actually populated by the last Read/SetRawPayload
// call), not the raw payload buffer capacity. The buffer is pooled and
// reused across messages, so anything beyond PayloadLength may be stale data
// left over from a previous message; treating it as readable would silently
// leak that stale content instead of failing.
func (m *TransporterMessage) readInt(offset uint32) (uint32, int, error) {
	typeSize := uint32(4)
	newOffset := offset + typeSize
	if m.PayloadLength() < newOffset {
		return 0, 0, fmt.Errorf("not enough data in the payload buffer, size: %d, offset: %d", m.PayloadLength(), newOffset)
	}
	value := ByteOrder.Uint32(m.payloadBuffer[offset:newOffset])
	return newOffset, int(value), nil
}

func (m *TransporterMessage) readString(offset uint32) (uint32, string, error) {
	lengthTypeSize := uint32(4)
	dataOffset := offset + lengthTypeSize
	if m.PayloadLength() < dataOffset {
		return 0, "", fmt.Errorf("not enough data in the payload buffer, size: %d, offset: %d", m.PayloadLength(), dataOffset)
	}
	length := ByteOrder.Uint32(m.payloadBuffer[offset:dataOffset])
	newOffset := dataOffset + length
	if m.PayloadLength() < newOffset {
		return 0, "", fmt.Errorf("not enough data in the payload buffer, size: %d, offset: %d", m.PayloadLength(), newOffset)
	}
	value := string(m.payloadBuffer[dataOffset:newOffset])
	return newOffset, value, nil
}

func (m *TransporterMessage) updatePayloadMetadata(payloadLength uint32) {
	targetBuffer := m.payloadBuffer[:payloadLength]
	ByteOrder.PutUint32(m.payloadLengthBuffer, payloadLength)
	ByteOrder.PutUint32(m.payloadCrc32Buffer, crc32.ChecksumIEEE(targetBuffer))
}

//endregion
