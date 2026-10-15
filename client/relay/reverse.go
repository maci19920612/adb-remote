package relay

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
// in vain), so the owner rewrites <local> to a relay endpoint of its own and
// hands every connection arriving there to the guest as an OPEN for the
// original <local>. The guest's adb-server, having seen the original
// request pass through it, accepts that OPEN and connects to <local>
// itself.
//
// "reverse:list-forward" is passed through unchanged, so it lists the relay
// endpoints rather than the guest's own <local> ends.
//
// Whatever connects to a relay endpoint is relayed to the guest as the
// device, so the endpoint is a Unix socket in a directory only this user
// can enter wherever adb-server can connect to one (everywhere but
// Windows); on Windows it is a loopback TCP port.
const (
	reverseForwardPrefix     = "reverse:forward:"
	reverseNoRebindPrefix    = "norebind:"
	reverseKillForwardPrefix = "reverse:killforward:"
	reverseKillForwardAll    = "reverse:killforward-all"
	reverseListForward       = "reverse:list-forward"
)

// reverseForward is one active reverse forward of the guest: device
// connections to remote arrive on listener, and are relayed to the guest's
// local.
type reverseForward struct {
	remote   string
	local    string // guarded by OwnerMultiplexer.mu; a rebind retargets it
	listener net.Listener
}

// endpoint returns the socket spec adb-server connects to for the relay.
func (f *reverseForward) endpoint() string {
	return relayEndpoint(f.listener)
}

func relayEndpoint(listener net.Listener) string {
	if address, ok := listener.Addr().(*net.UnixAddr); ok {
		return "localfilesystem:" + address.Name
	}
	return fmt.Sprintf("tcp:%d", listener.Addr().(*net.TCPAddr).Port)
}

// privateListener is a Unix socket listener in a directory of its own,
// which closing it removes.
type privateListener struct {
	net.Listener
	dir string
}

func (l *privateListener) Close() error {
	err := l.Listener.Close()
	_ = os.RemoveAll(l.dir)
	return err
}

// listenPrivately opens a Unix socket only this user can connect to.
func listenPrivately() (net.Listener, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("adb-server can't connect to Unix sockets on Windows")
	}
	// MkdirTemp creates the directory with mode 0700. The socket's path has
	// to stay short (about 100 bytes at most), hence the short names.
	dir, err := os.MkdirTemp("", "adb-remote-")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &privateListener{Listener: listener, dir: dir}, nil
}

// listenRelay opens a relay endpoint: a private Unix socket where
// possible, a loopback TCP port otherwise.
func (m *OwnerMultiplexer) listenRelay() (net.Listener, error) {
	listener, err := listenPrivately()
	if err == nil {
		return listener, nil
	}
	if runtime.GOOS != "windows" {
		m.logger.Warn(fmt.Sprintf("Couldn't open a private relay socket, falling back to a loopback port any local process can reach: %s", err))
	}
	return net.Listen("tcp", "127.0.0.1:0")
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
		// checkGuestService refuses these before they get here.
		return "", nil, fmt.Errorf("malformed reverse forward request %q", service)
	}
	rewrite := func(endpoint string) string {
		prefix := reverseForwardPrefix
		if norebind {
			prefix += reverseNoRebindPrefix
		}
		return fmt.Sprintf("%s%s;%s", prefix, remote, endpoint)
	}

	m.mu.Lock()
	existing := m.reverseForwards[remote]
	m.mu.Unlock()
	if existing != nil {
		// The device already sends remote to this relay endpoint; keep it
		// and only retarget. A norebind request is refused by the device,
		// since remote is already forwarded there.
		return rewrite(existing.endpoint()), func(opened bool) {
			if opened && !norebind {
				m.mu.Lock()
				existing.local = local
				m.mu.Unlock()
			}
		}, nil
	}

	listener, err := m.listenRelay()
	if err != nil {
		return "", nil, fmt.Errorf("failed to open a relay endpoint for the reverse forward: %w", err)
	}
	forward := &reverseForward{remote: remote, local: local, listener: listener}
	return rewrite(forward.endpoint()), func(opened bool) {
		if !opened {
			_ = listener.Close()
			return
		}
		m.mu.Lock()
		m.reverseForwards[remote] = forward
		m.mu.Unlock()
		m.logger.Info(fmt.Sprintf("Reverse forward %s -> guest's %s, relayed through %s", remote, local, forward.endpoint()))
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

// removeAllReverseForwards closes every relay endpoint and returns the
// forwards it removed.
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
// endpoint.
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
