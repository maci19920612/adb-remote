package tui

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/controller"
	"adb-remote.maci.team/client/transportLayer"
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type shareStage int

const (
	shareStageLoadingDevices shareStage = iota
	shareStageSelectDevice
	shareStageConnecting
	shareStageRoomActive
	shareStageError
)

const activityLogLimit = 10

// shareModel drives the `share` command's TUI: pick a local device (with
// refresh), then show the room id and handle join requests as they arrive.
type shareModel struct {
	ctx         context.Context
	smartSocket adb.IAdbSmartSocket
	autoAccept  bool

	// selectedDevice carries the chosen device id from Update (once) to
	// the background owner-flow goroutine.
	selectedDevice chan string

	stage   shareStage
	devices []adb.Device
	cursor  int
	err     error

	clientId string
	roomId   string

	pendingGuestId string
	pendingRespond chan<- bool

	activity []string
}

func newShareModel(ctx context.Context, smartSocket adb.IAdbSmartSocket, presetDevice string, autoAccept bool) *shareModel {
	m := &shareModel{
		ctx:            ctx,
		smartSocket:    smartSocket,
		autoAccept:     autoAccept,
		selectedDevice: make(chan string, 1),
		stage:          shareStageLoadingDevices,
	}
	if presetDevice != "" {
		m.stage = shareStageConnecting
		m.selectedDevice <- presetDevice
	}
	return m
}

// RunShare runs the interactive share TUI to completion. If presetDevice is
// non-empty, the device picker is skipped. If autoAccept is true, join
// requests are accepted automatically instead of prompting.
func RunShare(ctx context.Context, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, presetDevice string, autoAccept bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := newShareModel(ctx, smartSocket, presetDevice, autoAccept)
	program := tea.NewProgram(m)

	go runOwnerFlow(ctx, program, m, client, smartSocket, autoAccept)

	_, err := program.Run()
	cancel()
	if err != nil {
		return err
	}
	return m.err
}

// runOwnerFlow waits for a device to be selected, then performs the
// handshake and services the room, forwarding every state change into the
// TUI as a message.
func runOwnerFlow(ctx context.Context, program *tea.Program, m *shareModel, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, autoAccept bool) {
	var deviceId string
	select {
	case deviceId = <-m.selectedDevice:
	case <-ctx.Done():
		return
	}

	clientId, err := controller.Handshake(client)
	if err != nil {
		program.Send(shareErrorMsg{err})
		return
	}
	program.Send(clientIdMsg(clientId))

	promptAccept := func(guestClientId string) (bool, error) {
		if autoAccept {
			return true, nil
		}
		respond := make(chan bool, 1)
		program.Send(joinRequestMsg{clientId: guestClientId, respond: respond})
		select {
		case accepted := <-respond:
			return accepted, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	onEvent := func(e controller.OwnerEvent) {
		program.Send(ownerEventMsg(e))
	}

	if err := controller.JoinAsRoomOwner(ctx, client, smartSocket, deviceId, promptAccept, onEvent); err != nil && ctx.Err() == nil {
		program.Send(shareErrorMsg{err})
	}
}

// --- messages ---

type devicesLoadedMsg struct {
	devices []adb.Device
	err     error
}

type clientIdMsg string

type ownerEventMsg controller.OwnerEvent

type joinRequestMsg struct {
	clientId string
	respond  chan<- bool
}

type shareErrorMsg struct{ err error }

func fetchDevices(smartSocket adb.IAdbSmartSocket) tea.Cmd {
	return func() tea.Msg {
		devices, err := smartSocket.DeviceList()
		return devicesLoadedMsg{devices: devices, err: err}
	}
}

// --- bubbletea.Model ---

func (m *shareModel) Init() tea.Cmd {
	if m.stage == shareStageConnecting {
		return nil
	}
	return fetchDevices(m.smartSocket)
}

func (m *shareModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case devicesLoadedMsg:
		m.stage = shareStageSelectDevice
		m.devices = msg.devices
		m.err = msg.err
		if m.cursor >= len(m.devices) {
			m.cursor = 0
		}
		return m, nil
	case clientIdMsg:
		m.clientId = string(msg)
		return m, nil
	case ownerEventMsg:
		m.handleOwnerEvent(controller.OwnerEvent(msg))
		return m, nil
	case joinRequestMsg:
		m.pendingGuestId = msg.clientId
		m.pendingRespond = msg.respond
		return m, nil
	case shareErrorMsg:
		m.err = msg.err
		m.stage = shareStageError
		return m, nil
	}
	return m, nil
}

func (m *shareModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	if m.pendingRespond != nil {
		switch msg.String() {
		case "y":
			m.pendingRespond <- true
			m.pendingRespond = nil
			m.pendingGuestId = ""
		case "n":
			m.pendingRespond <- false
			m.pendingRespond = nil
			m.pendingGuestId = ""
		}
		return m, nil
	}

	switch m.stage {
	case shareStageSelectDevice:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.devices)-1 {
				m.cursor++
			}
		case "r":
			m.stage = shareStageLoadingDevices
			return m, fetchDevices(m.smartSocket)
		case "enter":
			if len(m.devices) > 0 {
				m.stage = shareStageConnecting
				m.selectedDevice <- m.devices[m.cursor].Id
			}
		case "q":
			return m, tea.Quit
		}
	case shareStageRoomActive, shareStageError, shareStageLoadingDevices, shareStageConnecting:
		if msg.String() == "q" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *shareModel) handleOwnerEvent(e controller.OwnerEvent) {
	switch e.Kind {
	case controller.OwnerRoomCreated:
		m.roomId = e.RoomId
		m.stage = shareStageRoomActive
	case controller.OwnerJoinRequested:
		m.appendActivity(fmt.Sprintf("Join request from clientId: %s", e.GuestClientId))
	case controller.OwnerJoinDecided:
		verb := "declined"
		if e.Accepted {
			verb = "accepted"
		}
		m.appendActivity(fmt.Sprintf("clientId %s: %s", e.GuestClientId, verb))
	case controller.OwnerJoinFailed:
		m.appendActivity(fmt.Sprintf("clientId %s: error handling join request: %s", e.GuestClientId, e.Err))
	}
}

func (m *shareModel) appendActivity(line string) {
	m.activity = append(m.activity, line)
	if len(m.activity) > activityLogLimit {
		m.activity = m.activity[len(m.activity)-activityLogLimit:]
	}
}

func (m *shareModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("adb-remote — share") + "\n")

	switch m.stage {
	case shareStageLoadingDevices:
		b.WriteString("Loading devices...\n")
	case shareStageSelectDevice:
		if m.err != nil {
			b.WriteString(errorStyle.Render(fmt.Sprintf("Error loading devices: %s", m.err)) + "\n\n")
		}
		if len(m.devices) == 0 {
			b.WriteString("No devices found.\n\n")
		}
		for i, d := range m.devices {
			line := fmt.Sprintf("%-24s %s", d.Id, d.Type)
			if i == m.cursor {
				b.WriteString(selectedStyle.Render("> "+line) + "\n")
			} else {
				b.WriteString("  " + line + "\n")
			}
		}
		b.WriteString("\n" + helpStyle.Render("↑/↓ move · enter select · r refresh · q quit"))
	case shareStageConnecting:
		b.WriteString("Connecting to the transporter...\n")
	case shareStageRoomActive:
		if m.clientId != "" {
			b.WriteString(labelStyle.Render("Your client id: ") + m.clientId + "\n")
		}
		b.WriteString(labelStyle.Render("Room id:        ") + successStyle.Render(m.roomId) + "\n\n")
		if m.pendingRespond != nil {
			b.WriteString(promptStyle.Render(fmt.Sprintf("Join request from clientId: %s — accept? [y/n]", m.pendingGuestId)) + "\n\n")
		} else {
			b.WriteString(dimStyle.Render("Waiting for guests to join...") + "\n\n")
		}
		if len(m.activity) > 0 {
			b.WriteString(labelStyle.Render("Activity:") + "\n")
			for _, line := range m.activity {
				b.WriteString("  " + line + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString(helpStyle.Render("q quit"))
	case shareStageError:
		b.WriteString(errorStyle.Render(fmt.Sprintf("Error: %s", m.err)) + "\n\n")
		b.WriteString(helpStyle.Render("q quit"))
	}

	return b.String()
}
