package tui

import (
	"adb-remote.maci.team/client/controller"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestConnectModel() *connectModel {
	return &connectModel{roomId: "ROOM42", localPort: "5038", stage: connectStageConnecting}
}

func TestConnectModelClientId(t *testing.T) {
	m := newTestConnectModel()
	updated, _ := m.Update(clientIdMsg("CLIENT1"))
	cm := updated.(*connectModel)
	if cm.clientId != "CLIENT1" {
		t.Fatalf("expected client id %q, got %q", "CLIENT1", cm.clientId)
	}
}

func TestConnectModelJoiningRoom(t *testing.T) {
	m := newTestConnectModel()
	updated, _ := m.Update(joiningRoomMsg{})
	cm := updated.(*connectModel)
	if cm.stage != connectStageJoiningRoom {
		t.Fatalf("expected stage %v, got %v", connectStageJoiningRoom, cm.stage)
	}
}

func TestConnectModelJoinAccepted(t *testing.T) {
	m := newTestConnectModel()
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestJoinDecided, Accepted: true})
	cm := updated.(*connectModel)
	if cm.stage != connectStageProxyStarting {
		t.Fatalf("expected stage %v, got %v", connectStageProxyStarting, cm.stage)
	}
}

func TestConnectModelJoinDenied(t *testing.T) {
	m := newTestConnectModel()
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestJoinDecided, Accepted: false})
	cm := updated.(*connectModel)
	if cm.stage != connectStageDenied {
		t.Fatalf("expected stage %v, got %v", connectStageDenied, cm.stage)
	}
}

func TestConnectModelProxyReady(t *testing.T) {
	m := newTestConnectModel()
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestProxyReady, LocalPort: "6000"})
	cm := updated.(*connectModel)
	if cm.stage != connectStageReady {
		t.Fatalf("expected stage %v, got %v", connectStageReady, cm.stage)
	}
	if cm.localPort != "6000" {
		t.Fatalf("expected local port %q, got %q", "6000", cm.localPort)
	}
}

func TestConnectModelLocalAdbConnectedIncrementsRelayCount(t *testing.T) {
	m := newTestConnectModel()
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestLocalAdbConnected})
	cm := updated.(*connectModel)
	if cm.stage != connectStageRelaying {
		t.Fatalf("expected stage %v, got %v", connectStageRelaying, cm.stage)
	}
	if cm.relayCount != 1 {
		t.Fatalf("expected relayCount 1, got %d", cm.relayCount)
	}
	updated, _ = cm.Update(guestEventMsg{Kind: controller.GuestLocalAdbConnected})
	cm = updated.(*connectModel)
	if cm.relayCount != 2 {
		t.Fatalf("expected relayCount 2 after a second connection, got %d", cm.relayCount)
	}
}

func TestConnectModelRelayStoppedGoesBackToReady(t *testing.T) {
	m := newTestConnectModel()
	m.stage = connectStageRelaying
	relayErr := errors.New("EOF")
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestRelayStopped, Err: relayErr})
	cm := updated.(*connectModel)
	if cm.stage != connectStageReady {
		t.Fatalf("expected stage %v, got %v", connectStageReady, cm.stage)
	}
	if cm.lastRelayErr != relayErr {
		t.Fatalf("expected the relay error to be recorded, got %v", cm.lastRelayErr)
	}
}

func TestConnectModelAutomaticAdbConnected(t *testing.T) {
	m := newTestConnectModel()
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestAdbConnected})
	cm := updated.(*connectModel)
	if !cm.adbConnected {
		t.Fatalf("expected adbConnected to be true")
	}
	if cm.adbConnectErr != nil {
		t.Fatalf("expected no adbConnectErr, got %v", cm.adbConnectErr)
	}
}

func TestConnectModelAutomaticAdbConnectFailed(t *testing.T) {
	m := newTestConnectModel()
	m.adbConnected = true // simulate a stale prior success before a later failure
	wantErr := errors.New("adb-server unreachable")
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestAdbConnectFailed, Err: wantErr})
	cm := updated.(*connectModel)
	if cm.adbConnected {
		t.Fatalf("expected adbConnected to be reset to false on failure")
	}
	if cm.adbConnectErr != wantErr {
		t.Fatalf("expected adbConnectErr %v, got %v", wantErr, cm.adbConnectErr)
	}
}

func TestConnectModelTransportLost(t *testing.T) {
	m := newTestConnectModel()
	m.stage = connectStageRelaying
	updated, _ := m.Update(guestEventMsg{Kind: controller.GuestTransportLost})
	cm := updated.(*connectModel)
	if cm.stage != connectStageDisconnected {
		t.Fatalf("expected stage %v, got %v", connectStageDisconnected, cm.stage)
	}
}

func TestConnectModelErrorStage(t *testing.T) {
	m := newTestConnectModel()
	wantErr := errors.New("transporter connection lost")
	updated, _ := m.Update(connectErrorMsg{wantErr})
	cm := updated.(*connectModel)
	if cm.stage != connectStageError || cm.err != wantErr {
		t.Fatalf("expected error stage with %v, got stage=%v err=%v", wantErr, cm.stage, cm.err)
	}
}

func TestConnectModelQuit(t *testing.T) {
	m := newTestConnectModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatalf("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected the command to produce tea.QuitMsg")
	}
}

func TestConnectModelCtrlCQuits(t *testing.T) {
	m := newTestConnectModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected the command to produce tea.QuitMsg")
	}
}
