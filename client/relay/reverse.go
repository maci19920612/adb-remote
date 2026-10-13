package relay

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

// Reverse forwards ("adb reverse", used by scrcpy and Android Studio's
// device mirroring to let the device connect back to the host) are the one
// place where the device opens streams towards the host instead of the
// other way around. The guest requests one as
// "reverse:forward:[norebind:]<remote>;<local>", where <remote> is a socket
// on the device and <local> a socket on the guest's machine. Passing that
// to the owner's adb-server unchanged would make it connect to <local> on
// the owner's machine, where nothing listens (and the guest's scrcpy waits
// in vain), so the owner rewrites <local> to a relay port of its own and
// hands every connection arriving there to the guest as an OPEN for the
// original <local>. The guest's adb-server, having seen the original
// request pass through it, accepts that OPEN and connects to <local>
// itself.
//
// "reverse:list-forward" is passed through unchanged, so it lists the relay
// ports rather than the guest's own <local> ends.
const (
	reverseForwardPrefix     = "reverse:forward:"
	reverseNoRebindPrefix    = "norebind:"
	reverseKillForwardPrefix = "reverse:killforward:"
	reverseKillForwardAll    = "reverse:killforward-all"
)

// reverseForward is one active reverse forward of the guest: device
// connections to remote arrive on listener, and are relayed to the guest's
// local.
type reverseForward struct {
	remote   string
	local    string // guarded by OwnerMultiplexer.mu; a rebind retargets it
	listener net.Listener
}

func (f *reverseForward) port() int {
	return f.listener.Addr().(*net.TCPAddr).Port
}

// openDeviceStream opens service on the local device, translating reverse
// forward requests as described above.
func (m *OwnerMultiplexer) openDeviceStream(service string) (net.Conn, error) {
	deviceService, onOpened, err := m.prepareService(service)
	if err != nil {
		return nil, err
	}
	conn, err := m.smartSocket.OpenStream(m.deviceId, deviceService)
	onOpened(err == nil)
	return conn, err
}

// prepareService returns the service to request from the device in place
// of service, and a callback to run once that request was made, reporting
// whether the device accepted the stream.
func (m *OwnerMultiplexer) prepareService(service string) (string, func(opened bool), error) {
	switch {
	case strings.HasPrefix(service, reverseForwardPrefix):
		return m.prepareReverseForward(service)
	case strings.HasPrefix(service, reverseKillForwardPrefix):
		remote := strings.TrimPrefix(service, reverseKillForwardPrefix)
		return service, func(opened bool) {
			if opened {
				m.removeReverseForward(remote)
			}
		}, nil
	case service == reverseKillForwardAll:
		return service, func(opened bool) {
			if opened {
				m.removeAllReverseForwards()
			}
		}, nil
	}
	return service, func(bool) {}, nil
}

func (m *OwnerMultiplexer) prepareReverseForward(service string) (string, func(opened bool), error) {
	spec := strings.TrimPrefix(service, reverseForwardPrefix)
	norebind := strings.HasPrefix(spec, reverseNoRebindPrefix)
	remote, local, ok := strings.Cut(strings.TrimPrefix(spec, reverseNoRebindPrefix), ";")
	if !ok {
		// Malformed; let the device reject it.
		return service, func(bool) {}, nil
	}
	rewrite := func(port int) string {
		prefix := reverseForwardPrefix
		if norebind {
			prefix += reverseNoRebindPrefix
		}
		return fmt.Sprintf("%s%s;tcp:%d", prefix, remote, port)
	}

	m.mu.Lock()
	existing := m.reverseForwards[remote]
	m.mu.Unlock()
	if existing != nil {
		// The device already sends remote to this relay port; keep it and
		// only retarget. A norebind request is refused by the device, since
		// remote is already forwarded there.
		return rewrite(existing.port()), func(opened bool) {
			if opened && !norebind {
				m.mu.Lock()
				existing.local = local
				m.mu.Unlock()
			}
		}, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("failed to open a relay port for the reverse forward: %w", err)
	}
	forward := &reverseForward{remote: remote, local: local, listener: listener}
	return rewrite(forward.port()), func(opened bool) {
		if !opened {
			_ = listener.Close()
			return
		}
		m.mu.Lock()
		m.reverseForwards[remote] = forward
		m.mu.Unlock()
		m.logger.Info(fmt.Sprintf("Reverse forward %s -> guest's %s, relayed through 127.0.0.1:%d", remote, local, forward.port()))
		go m.acceptReverseConnections(forward)
	}, nil
}

// acceptReverseConnections hands every device connection arriving through
// forward to the guest, until the forward is removed.
func (m *OwnerMultiplexer) acceptReverseConnections(forward *reverseForward) {
	for {
		conn, err := forward.listener.Accept()
		if err != nil {
			return
		}
		m.openGuestStream(forward, conn)
	}
}

// openGuestStream registers conn as a new stream and asks the guest to open
// the forward's local end for it. Relaying starts once the guest
// acknowledges the OPEN (see handleOkay).
func (m *OwnerMultiplexer) openGuestStream(forward *reverseForward, conn net.Conn) {
	stream := &ownerStream{
		ownId:         atomic.AddUint32(&m.nextId, 1),
		awaitingGuest: true,
		conn:          conn,
		sendPermit:    make(chan struct{}, 1),
		done:          make(chan struct{}),
	}
	stream.sendPermit <- struct{}{}

	m.mu.Lock()
	if m.reverseForwards[forward.remote] != forward {
		// Removed while this connection was being accepted; checked under
		// the same lock as the registration, so Close can't miss it.
		m.mu.Unlock()
		_ = conn.Close()
		return
	}
	local := forward.local
	m.streams[stream.ownId] = stream
	m.mu.Unlock()

	m.logger.Info(fmt.Sprintf("Device connected to reverse forward %s, opening the guest's %s (id=%d)", forward.remote, local, stream.ownId))
	if err := m.sendOpen(stream.ownId, local); err != nil {
		m.logger.Error(fmt.Sprintf("Failed to ask the guest to open %s: %s", local, err))
		m.closeStream(stream, false)
	}
}

func (m *OwnerMultiplexer) removeReverseForward(remote string) {
	m.mu.Lock()
	forward := m.reverseForwards[remote]
	delete(m.reverseForwards, remote)
	m.mu.Unlock()
	if forward != nil {
		_ = forward.listener.Close()
	}
}

// removeAllReverseForwards closes every relay port and returns the forwards
// it removed.
func (m *OwnerMultiplexer) removeAllReverseForwards() []*reverseForward {
	m.mu.Lock()
	forwards := make([]*reverseForward, 0, len(m.reverseForwards))
	for _, forward := range m.reverseForwards {
		forwards = append(forwards, forward)
	}
	clear(m.reverseForwards)
	m.mu.Unlock()

	for _, forward := range forwards {
		_ = forward.listener.Close()
	}
	return forwards
}

// killReverseForwardOnDevice removes remote's forward from the device, so a
// later device connection to it can't end up on whatever reuses the relay
// port.
func (m *OwnerMultiplexer) killReverseForwardOnDevice(remote string) {
	conn, err := m.smartSocket.OpenStream(m.deviceId, reverseKillForwardPrefix+remote)
	if err != nil {
		m.logger.Error(fmt.Sprintf("Failed to remove reverse forward %s from the device: %s", remote, err))
		return
	}
	defer conn.Close()
	// Wait for the device's OKAY/FAIL, so the request has been handled
	// before the stream is closed.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.ReadFull(conn, make([]byte, 4))
}
