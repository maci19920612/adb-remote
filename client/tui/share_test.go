package tui

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/controller"
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestShareModelInitFetchesDevicesWhenNoPreset(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	if m.stage != shareStageLoadingDevices {
		t.Fatalf("expected stage %v, got %v", shareStageLoadingDevices, m.stage)
	}
	if m.Init() == nil {
		t.Fatalf("expected Init to return a fetch command")
	}
}

func TestShareModelInitSkipsPickerWithPresetDevice(t *testing.T) {
	m := newShareModel(context.Background(), nil, "emulator-5554", false, "FP-TEST", nil)
	if m.stage != shareStageConnecting {
		t.Fatalf("expected stage %v, got %v", shareStageConnecting, m.stage)
	}
	if cmd := m.Init(); cmd == nil {
		// Init always starts the transfer-stats ticker regardless of stage;
		// what a preset device skips is fetchDevices specifically.
		t.Fatalf("expected the transfer-stats ticker command even with a preset device")
	}
	select {
	case device := <-m.selectedDevice:
		if device != "emulator-5554" {
			t.Fatalf("expected the preset device id, got %q", device)
		}
	default:
		t.Fatalf("expected the preset device id to already be queued")
	}
}

func TestShareModelDevicesLoadedPopulatesList(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	devices := []adb.Device{{Id: "emulator-5554", Type: adb.TypeDevice}, {Id: "R58M", Type: adb.TypeDevice}}
	updated, _ := m.Update(devicesLoadedMsg{devices: devices})
	sm := updated.(*shareModel)
	if sm.stage != shareStageSelectDevice {
		t.Fatalf("expected stage %v, got %v", shareStageSelectDevice, sm.stage)
	}
	if len(sm.devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(sm.devices))
	}
}

func TestShareModelDevicesLoadedError(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	wantErr := errors.New("adb not running")
	updated, _ := m.Update(devicesLoadedMsg{err: wantErr})
	sm := updated.(*shareModel)
	if sm.err != wantErr {
		t.Fatalf("expected the load error to be recorded, got %v", sm.err)
	}
}

func TestShareModelCursorNavigation(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageSelectDevice
	m.devices = []adb.Device{{Id: "a"}, {Id: "b"}, {Id: "c"}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*shareModel)
	if m.cursor != 1 {
		t.Fatalf("expected cursor 1, got %d", m.cursor)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*shareModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown}) // should clamp at the end
	m = updated.(*shareModel)
	if m.cursor != 2 {
		t.Fatalf("expected cursor to clamp at 2, got %d", m.cursor)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*shareModel)
	if m.cursor != 1 {
		t.Fatalf("expected cursor 1 after up, got %d", m.cursor)
	}
}

func TestShareModelEnterSelectsDevice(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageSelectDevice
	m.devices = []adb.Device{{Id: "emulator-5554"}, {Id: "R58M"}}
	m.cursor = 1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*shareModel)
	if m.stage != shareStageConnecting {
		t.Fatalf("expected stage %v, got %v", shareStageConnecting, m.stage)
	}
	select {
	case device := <-m.selectedDevice:
		if device != "R58M" {
			t.Fatalf("expected the highlighted device id, got %q", device)
		}
	default:
		t.Fatalf("expected the selected device id to be queued")
	}
}

func TestShareModelRefreshReturnsFetchCommand(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageSelectDevice
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(*shareModel)
	if m.stage != shareStageLoadingDevices {
		t.Fatalf("expected stage %v, got %v", shareStageLoadingDevices, m.stage)
	}
	if cmd == nil {
		t.Fatalf("expected a refresh command")
	}
}

func TestShareModelHandlesOwnerRoomCreated(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	updated, _ := m.Update(ownerEventMsg{Kind: controller.OwnerRoomCreated, RoomId: "ROOM42"})
	m = updated.(*shareModel)
	if m.stage != shareStageRoomActive {
		t.Fatalf("expected stage %v, got %v", shareStageRoomActive, m.stage)
	}
	if m.roomId != "ROOM42" {
		t.Fatalf("expected room id %q, got %q", "ROOM42", m.roomId)
	}
}

func TestShareModelLogsJoinActivity(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	updated, _ := m.Update(ownerEventMsg{Kind: controller.OwnerJoinRequested, GuestClientId: "GUEST1"})
	m = updated.(*shareModel)
	updated, _ = m.Update(ownerEventMsg{Kind: controller.OwnerJoinDecided, GuestClientId: "GUEST1", Accepted: true})
	m = updated.(*shareModel)
	if len(m.activity) != 2 {
		t.Fatalf("expected 2 activity lines, got %d: %v", len(m.activity), m.activity)
	}
}

func TestShareModelTracksConnectedGuestOnAccept(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	guestPublicKey := []byte{0x01, 0x02, 0x03}
	updated, _ := m.Update(ownerEventMsg{Kind: controller.OwnerJoinDecided, GuestClientId: "GUEST1", GuestPublicKey: guestPublicKey, Accepted: true})
	m = updated.(*shareModel)
	if m.connectedGuestId != "GUEST1" {
		t.Fatalf("expected the connected guest id to be recorded, got %q", m.connectedGuestId)
	}
	if m.connectedGuestFingerprint == "" {
		t.Fatalf("expected the connected guest's fingerprint to be recorded")
	}
}

func TestShareModelDoesNotTrackConnectedGuestOnDecline(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	updated, _ := m.Update(ownerEventMsg{Kind: controller.OwnerJoinDecided, GuestClientId: "GUEST1", Accepted: false})
	m = updated.(*shareModel)
	if m.connectedGuestId != "" {
		t.Fatalf("expected no connected guest to be recorded on decline, got %q", m.connectedGuestId)
	}
}

func TestShareModelClearsConnectedGuestOnGuestLeft(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	updated, _ := m.Update(ownerEventMsg{Kind: controller.OwnerJoinDecided, GuestClientId: "GUEST1", GuestPublicKey: []byte{1, 2, 3}, Accepted: true})
	m = updated.(*shareModel)
	if m.connectedGuestId != "GUEST1" {
		t.Fatalf("expected GUEST1 to be recorded as connected first")
	}

	updated, _ = m.Update(ownerEventMsg{Kind: controller.OwnerGuestLeft})
	m = updated.(*shareModel)
	if m.connectedGuestId != "" || m.connectedGuestFingerprint != "" {
		t.Fatalf("expected the connected guest to be cleared after it left, got %+v", m)
	}
	if len(m.activity) == 0 {
		t.Fatalf("expected the guest leaving to be logged")
	}
}

func TestShareModelGuestLeftClearsPendingPrompt(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageRoomActive
	respond := make(chan bool, 1)
	updated, _ := m.Update(joinRequestMsg{clientId: "GUEST1", fingerprint: "FP-GUEST", respond: respond})
	m = updated.(*shareModel)

	updated, _ = m.Update(ownerEventMsg{Kind: controller.OwnerGuestLeft})
	m = updated.(*shareModel)
	if m.pendingGuestId != "" || m.pendingRespond != nil {
		t.Fatalf("expected the pending join prompt to be cleared when the guest left, got %+v", m)
	}
	select {
	case accepted := <-respond:
		if accepted {
			t.Fatalf("expected the abandoned prompt to resolve to declined")
		}
	default:
		t.Fatalf("expected the blocked promptAccept goroutine to be unblocked")
	}
}

func TestShareModelJoinRequestPromptAcceptDecline(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageRoomActive
	respond := make(chan bool, 1)

	updated, _ := m.Update(joinRequestMsg{clientId: "GUEST1", respond: respond})
	m = updated.(*shareModel)
	if m.pendingGuestId != "GUEST1" || m.pendingRespond == nil {
		t.Fatalf("expected a pending join request for GUEST1, got %+v", m)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(*shareModel)
	if m.pendingRespond != nil {
		t.Fatalf("expected the pending request to be cleared after answering")
	}
	select {
	case accepted := <-respond:
		if !accepted {
			t.Fatalf("expected 'y' to accept")
		}
	default:
		t.Fatalf("expected an answer to be sent on the respond channel")
	}
}

func TestShareModelJoinRequestDecline(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageRoomActive
	respond := make(chan bool, 1)
	updated, _ := m.Update(joinRequestMsg{clientId: "GUEST1", respond: respond})
	m = updated.(*shareModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(*shareModel)
	select {
	case accepted := <-respond:
		if accepted {
			t.Fatalf("expected 'n' to decline")
		}
	default:
		t.Fatalf("expected an answer to be sent on the respond channel")
	}
}

func TestShareModelSessionTimeoutQuits(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageRoomActive
	updated, cmd := m.Update(sessionTimeoutMsg{})
	sm := updated.(*shareModel)
	if sm.stage != shareStageSessionTimeout {
		t.Fatalf("expected stage %v, got %v", shareStageSessionTimeout, sm.stage)
	}
	if cmd == nil {
		t.Fatalf("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected the command to produce tea.QuitMsg")
	}
}

func TestShareModelErrorStage(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	wantErr := errors.New("transporter connection lost")
	updated, _ := m.Update(shareErrorMsg{wantErr})
	m = updated.(*shareModel)
	if m.stage != shareStageError || m.err != wantErr {
		t.Fatalf("expected error stage with %v, got stage=%v err=%v", wantErr, m.stage, m.err)
	}
}

func TestShareModelQuit(t *testing.T) {
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageSelectDevice
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatalf("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected the command to produce tea.QuitMsg")
	}
}

func TestShareModelPendingRespondSwallowsQuit(t *testing.T) {
	// While a join request is pending, 'q' isn't a recognized answer and
	// must not be treated as global quit either (only y/n/ctrl+c apply),
	// so the operator can't accidentally exit the TUI mid-decision without
	// noticing.
	m := newShareModel(context.Background(), nil, "", false, "FP-TEST", nil)
	m.stage = shareStageRoomActive
	respond := make(chan bool, 1)
	m.pendingGuestId = "GUEST1"
	m.pendingRespond = respond

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		t.Fatalf("expected 'q' to be ignored while a join request is pending")
	}
	select {
	case <-respond:
		t.Fatalf("expected no answer to be sent for an unrecognized key")
	default:
	}
}
