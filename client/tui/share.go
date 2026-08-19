package tui

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/controller"
	"adb-remote.maci.team/client/identity"
	"adb-remote.maci.team/client/transportLayer"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type shareStage int

const (
	shareStageLoadingDevices shareStage = iota
	shareStageSelectDevice
	shareStageConnecting
	shareStageRoomActive
	shareStageSessionTimeout
	shareStageError
)

const activityLogLimit = 10

// shareModel drives the `share` command's TUI: pick a local device (with
// refresh), then show the room id and handle join requests as they arrive.
type shareModel struct {
	ctx         context.Context
	smartSocket adb.IAdbSmartSocket
	autoAccept  bool
	fingerprint string

	// selectedDevice carries the chosen device id from Update (once) to
	// the background owner-flow goroutine.
	selectedDevice chan string

	stage   shareStage
	devices []adb.Device
	cursor  int
	err     error

	clientId string
	roomId   string

	pendingGuestId     string
	pendingFingerprint string
	pendingRespond     chan<- bool

	// connectedGuestId/connectedGuestFingerprint identify the room's
	// current guest, since a room holds exactly one guest at a time; they
	// stick around (rather than fading into the activity log) for as long
	// as that guest is connected.
	connectedGuestId          string
	connectedGuestFingerprint string

	activity []string

	statsSource   transferStatsSource
	stats         transferStats
	width, height int
}

func newShareModel(ctx context.Context, smartSocket adb.IAdbSmartSocket, presetDevice string, autoAccept bool, fingerprint string, statsSource transferStatsSource) *shareModel {
	m := &shareModel{
		ctx:            ctx,
		smartSocket:    smartSocket,
		autoAccept:     autoAccept,
		fingerprint:    fingerprint,
		selectedDevice: make(chan string, 1),
		stage:          shareStageLoadingDevices,
		statsSource:    statsSource,
	}
	if presetDevice != "" {
		m.stage = shareStageConnecting
		m.selectedDevice <- presetDevice
	}
	return m
}

// RunShare runs the interactive share TUI to completion. If presetDevice is
// non-empty, the device picker is skipped. If autoAccept is true, join
// requests are accepted automatically instead of prompting. sessionTimeout
// closes the room (and this process) once it elapses after the room is
// created; a zero or negative value (including the documented -1 CLI
// sentinel) disables the timeout.
func RunShare(ctx context.Context, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, ownerIdentity *identity.Identity, presetDevice string, autoAccept bool, sessionTimeout time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := newShareModel(ctx, smartSocket, presetDevice, autoAccept, ownerIdentity.Fingerprint(), client)
	program := tea.NewProgram(m, tea.WithAltScreen())

	go runOwnerFlow(ctx, program, m, client, smartSocket, ownerIdentity, autoAccept, sessionTimeout)

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
func runOwnerFlow(ctx context.Context, program *tea.Program, m *shareModel, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, ownerIdentity *identity.Identity, autoAccept bool, sessionTimeout time.Duration) {
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

	promptAccept := func(guestClientId string, guestPublicKey []byte) (bool, error) {
		if autoAccept {
			return true, nil
		}
		respond := make(chan bool, 1)
		program.Send(joinRequestMsg{clientId: guestClientId, fingerprint: identity.Fingerprint(guestPublicKey), respond: respond})
		select {
		case accepted := <-respond:
			return accepted, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	// ownerCtx derives from ctx so the timeout can stop JoinAsRoomOwner (and
	// so close the room) without needing the outer RunShare's cancel func;
	// timedOut distinguishes an expiry-triggered stop from any other reason
	// ownerCtx might end up cancelled, so the generic error path below
	// doesn't clobber the timeout message already sent to the TUI.
	ownerCtx, cancelOwner := context.WithCancel(ctx)
	defer cancelOwner()
	var timedOut atomic.Bool
	if sessionTimeout > 0 {
		timer := time.AfterFunc(sessionTimeout, func() {
			timedOut.Store(true)
			program.Send(sessionTimeoutMsg{})
			cancelOwner()
		})
		defer timer.Stop()
	}

	onEvent := func(e controller.OwnerEvent) {
		program.Send(ownerEventMsg(e))
	}

	if err := controller.JoinAsRoomOwner(ownerCtx, client, smartSocket, deviceId, ownerIdentity, promptAccept, onEvent); err != nil && ctx.Err() == nil && !timedOut.Load() {
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
	clientId    string
	fingerprint string
	respond     chan<- bool
}

type shareErrorMsg struct{ err error }

type sessionTimeoutMsg struct{}

func fetchDevices(smartSocket adb.IAdbSmartSocket) tea.Cmd {
	return func() tea.Msg {
		devices, err := smartSocket.DeviceList()
		return devicesLoadedMsg{devices: devices, err: err}
	}
}

// --- bubbletea.Model ---

func (m *shareModel) Init() tea.Cmd {
	if m.stage == shareStageConnecting {
		return tickTransferStats()
	}
	return tea.Batch(fetchDevices(m.smartSocket), tickTransferStats())
}

func (m *shareModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case transferTickMsg:
		return m, m.stats.sample(m.statsSource, time.Time(msg))
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
		m.pendingFingerprint = msg.fingerprint
		m.pendingRespond = msg.respond
		return m, nil
	case shareErrorMsg:
		m.err = msg.err
		m.stage = shareStageError
		return m, nil
	case sessionTimeoutMsg:
		m.stage = shareStageSessionTimeout
		return m, tea.Quit
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
			m.pendingFingerprint = ""
		case "n":
			m.pendingRespond <- false
			m.pendingRespond = nil
			m.pendingGuestId = ""
			m.pendingFingerprint = ""
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
		m.appendActivity(fmt.Sprintf("Join request from clientId: %s (fingerprint %s)", e.GuestClientId, identity.Fingerprint(e.GuestPublicKey)))
	case controller.OwnerJoinDecided:
		verb := "declined"
		if e.Accepted {
			verb = "accepted"
			m.connectedGuestId = e.GuestClientId
			m.connectedGuestFingerprint = identity.Fingerprint(e.GuestPublicKey)
		}
		m.appendActivity(fmt.Sprintf("clientId %s: %s", e.GuestClientId, verb))
	case controller.OwnerJoinFailed:
		m.appendActivity(fmt.Sprintf("clientId %s: error handling join request: %s", e.GuestClientId, e.Err))
	case controller.OwnerGuestLeft:
		// Only one guest is ever active at a time, so whichever one we were
		// tracking (connected, or still-pending a decision) is the one that
		// left.
		leftId := m.connectedGuestId
		if leftId == "" {
			leftId = m.pendingGuestId
		}
		m.appendActivity(fmt.Sprintf("clientId %s: disconnected", leftId))
		m.connectedGuestId = ""
		m.connectedGuestFingerprint = ""
		if m.pendingRespond != nil {
			// Unblock the goroutine waiting on this decision instead of
			// leaking it for the rest of the session; the decision is moot
			// now, and handleJoinRequest's resulting SendJoinRoomResponse
			// is harmless — the transporter just reports the guest is gone.
			m.pendingRespond <- false
		}
		m.pendingGuestId = ""
		m.pendingFingerprint = ""
		m.pendingRespond = nil
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
		b.WriteString(labelStyle.Render("Your fingerprint: ") + m.fingerprint + "\n")
		b.WriteString(labelStyle.Render("Room id:        ") + successStyle.Render(m.roomId) + "\n\n")
		if m.pendingRespond != nil {
			b.WriteString(promptStyle.Render(fmt.Sprintf("Join request from clientId: %s — accept? [y/n]", m.pendingGuestId)) + "\n")
			b.WriteString(labelStyle.Render("  Guest fingerprint: ") + m.pendingFingerprint + "\n")
			b.WriteString(dimStyle.Render("  Verify this matches the guest's own displayed fingerprint out of band before accepting.") + "\n\n")
		} else if m.connectedGuestId != "" {
			b.WriteString(labelStyle.Render("Connected guest: ") + successStyle.Render(m.connectedGuestId) + "\n")
			b.WriteString(labelStyle.Render("  Guest fingerprint: ") + m.connectedGuestFingerprint + "\n\n")
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
	case shareStageSessionTimeout:
		b.WriteString(errorStyle.Render("Session timeout reached — closing the room.") + "\n\n")
	case shareStageError:
		b.WriteString(errorStyle.Render(fmt.Sprintf("Error: %s", m.err)) + "\n\n")
		b.WriteString(helpStyle.Render("q quit"))
	}

	return layoutWithFooter(b.String(), m.stats.render(), m.height)
}
