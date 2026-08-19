package controller

import (
	"adb-remote.maci.team/shared/protocol"
	"strings"
	"testing"
)

func TestHandshakeSuccess(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan error, 1)
	go func() { done <- Handshake(client) }()

	request := readMessage(t, server)
	if request.Command() != protocol.CommandConnect {
		t.Fatalf("expected command %x, got %x", protocol.CommandConnect, request.Command())
	}

	response := protocol.CreateTransporterMessage()
	response.SetResponseCommand(protocol.CommandConnect)
	if err := response.SetPayloadConnectResponse(&protocol.TransporterMessagePayloadConnectResponse{ClientId: "CLIENT1"}); err != nil {
		t.Fatalf("SetPayloadConnectResponse failed: %s", err)
	}
	if err := response.Write(server); err != nil {
		t.Fatalf("failed to write the response: %s", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Handshake failed: %s", err)
	}
}

func TestHandshakeReportsServerError(t *testing.T) {
	client, server := newConnectedClient(t)

	done := make(chan error, 1)
	go func() { done <- Handshake(client) }()

	readMessage(t, server)

	response := protocol.CreateTransporterMessage()
	response.SetErrorResponseCommand(protocol.CommandConnect)
	if err := response.SetErrorPayload(&protocol.TransporterMessagePayloadError{
		ErrorCode:    protocol.ErrorProtocolNotSupported,
		ErrorMessage: "unsupported protocol version",
	}); err != nil {
		t.Fatalf("SetErrorPayload failed: %s", err)
	}
	if err := response.Write(server); err != nil {
		t.Fatalf("failed to write the response: %s", err)
	}

	err := <-done
	if err == nil {
		t.Fatalf("expected Handshake to fail")
	}
	if !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("expected the error to mention the server message, got: %s", err)
	}
}
