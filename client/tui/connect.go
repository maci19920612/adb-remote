package tui

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/controller"
	"adb-remote.maci.team/client/identity"
	"adb-remote.maci.team/client/transportLayer"
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type connectStage int

const (
	connectStageConnecting connectStage = iota
	connectStageJoiningRoom
	connectStageDenied
	connectStageProxyStarting
	connectStageReady
	connectStageRelaying
	connectStageError
)

// connectModel drives the `connect` command's TUI: report the assigned
// client id and the connection state as the guest joins the room, starts
// the local proxy, and relays traffic.
type connectModel struct {
	roomId      string
	localPort   string
	fingerprint string

	stage    connectStage
	clientId string
	err      error

	adbConnected  bool
	adbConnectErr error

	relayCount   int
	lastRelayErr error
}

// RunConnect runs the interactive connect TUI to completion. It does not
// return until the background guest flow (including its "adb disconnect"
// cleanup) has fully stopped, so callers can rely on cleanup having
// happened by the time this returns.
func RunConnect(ctx context.Context, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, guestIdentity *identity.Identity, roomId string, localPort string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := &connectModel{roomId: roomId, localPort: localPort, fingerprint: guestIdentity.Fingerprint(), stage: connectStageConnecting}
	program := tea.NewProgram(m)

	guestFlowDone := make(chan struct{})
	go func() {
		defer close(guestFlowDone)
		runGuestFlow(ctx, program, client, smartSocket, guestIdentity, roomId, localPort)
	}()

	_, err := program.Run()
	cancel()
	<-guestFlowDone // let cleanup (e.g. the automatic "adb disconnect") finish before returning

	if err != nil {
		return err
	}
	return m.err
}

func runGuestFlow(ctx context.Context, program *tea.Program, client *transportLayer.Client, smartSocket adb.IAdbSmartSocket, guestIdentity *identity.Identity, roomId string, localPort string) {
	clientId, err := controller.Handshake(client)
	if err != nil {
		program.Send(connectErrorMsg{err})
		return
	}
	program.Send(clientIdMsg(clientId))
	program.Send(joiningRoomMsg{})

	onEvent := func(e controller.GuestEvent) {
		program.Send(guestEventMsg(e))
	}

	err = controller.JoinAsGuest(ctx, client, smartSocket, guestIdentity, roomId, localPort, onEvent)
	if err == nil || ctx.Err() != nil {
		return
	}
	var denied *controller.ErrJoinRoomDenied
	if errors.As(err, &denied) {
		// Already reflected via a GuestJoinDecided{Accepted:false} event.
		return
	}
	program.Send(connectErrorMsg{err})
}

// --- messages ---

type joiningRoomMsg struct{}
type guestEventMsg controller.GuestEvent
type connectErrorMsg struct{ err error }

// --- bubbletea.Model ---

func (m *connectModel) Init() tea.Cmd {
	return nil
}

func (m *connectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
	case clientIdMsg:
		m.clientId = string(msg)
	case joiningRoomMsg:
		m.stage = connectStageJoiningRoom
	case guestEventMsg:
		m.handleGuestEvent(controller.GuestEvent(msg))
	case connectErrorMsg:
		m.err = msg.err
		m.stage = connectStageError
	}
	return m, nil
}

func (m *connectModel) handleGuestEvent(e controller.GuestEvent) {
	switch e.Kind {
	case controller.GuestJoinDecided:
		if e.Accepted {
			m.stage = connectStageProxyStarting
		} else {
			m.stage = connectStageDenied
		}
	case controller.GuestProxyReady:
		m.stage = connectStageReady
		m.localPort = e.LocalPort
	case controller.GuestAdbConnected:
		m.adbConnected = true
		m.adbConnectErr = nil
	case controller.GuestAdbConnectFailed:
		m.adbConnected = false
		m.adbConnectErr = e.Err
	case controller.GuestLocalAdbConnected:
		m.stage = connectStageRelaying
		m.relayCount++
	case controller.GuestRelayStopped:
		m.lastRelayErr = e.Err
		m.stage = connectStageReady
	}
}

func (m *connectModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("adb-remote — connect") + "\n")

	if m.clientId != "" {
		b.WriteString(labelStyle.Render("Your client id: ") + m.clientId + "\n")
	}
	b.WriteString(labelStyle.Render("Your fingerprint: ") + m.fingerprint + "\n")
	b.WriteString(dimStyle.Render("  Share this with the room owner so they can verify it's really you accepting.") + "\n")
	b.WriteString(labelStyle.Render("Room id:        ") + m.roomId + "\n\n")
	b.WriteString(labelStyle.Render("Connection state: ") + m.stateLine() + "\n\n")

	switch m.stage {
	case connectStageReady, connectStageRelaying:
		b.WriteString(fmt.Sprintf("Local proxy: 127.0.0.1:%s\n", m.localPort))
		if m.adbConnected {
			b.WriteString(successStyle.Render("adb connect issued automatically") + "\n\n")
		} else if m.adbConnectErr != nil {
			b.WriteString(errorStyle.Render(fmt.Sprintf("Automatic \"adb connect\" failed: %s", m.adbConnectErr)) + "\n")
			b.WriteString(dimStyle.Render(fmt.Sprintf("Run it yourself: adb connect 127.0.0.1:%s", m.localPort)) + "\n\n")
		} else {
			b.WriteString("\n")
		}
		if m.relayCount > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("(%d local adb connection(s) relayed so far)", m.relayCount)) + "\n\n")
		}
		if m.lastRelayErr != nil {
			b.WriteString(dimStyle.Render(fmt.Sprintf("Last relay ended: %s", m.lastRelayErr)) + "\n\n")
		}
	case connectStageDenied:
		b.WriteString(errorStyle.Render("The room owner declined the join request.") + "\n\n")
	case connectStageError:
		b.WriteString(errorStyle.Render(fmt.Sprintf("Error: %s", m.err)) + "\n\n")
	}

	b.WriteString(helpStyle.Render("q quit"))
	return b.String()
}

func (m *connectModel) stateLine() string {
	switch m.stage {
	case connectStageConnecting:
		return "connecting to the transporter..."
	case connectStageJoiningRoom:
		return "joining room " + m.roomId + "..."
	case connectStageDenied:
		return errorStyle.Render("join request declined")
	case connectStageProxyStarting:
		return "join accepted, starting the local proxy..."
	case connectStageReady:
		return successStyle.Render("ready — waiting for a local adb connection")
	case connectStageRelaying:
		return successStyle.Render("relaying ADB traffic")
	case connectStageError:
		return errorStyle.Render("error")
	default:
		return ""
	}
}
