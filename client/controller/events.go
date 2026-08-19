package controller

// OwnerEventKind identifies what happened during JoinAsRoomOwner.
type OwnerEventKind int

const (
	// OwnerRoomCreated reports the room id once the room has been created.
	OwnerRoomCreated OwnerEventKind = iota
	// OwnerJoinRequested reports that a guest asked to join, before
	// promptAccept has decided anything.
	OwnerJoinRequested
	// OwnerJoinDecided reports the accept/decline decision for a
	// previously reported OwnerJoinRequested.
	OwnerJoinDecided
	// OwnerJoinFailed reports that handling a join request itself failed
	// (bad payload, promptAccept error, or the response couldn't be sent).
	OwnerJoinFailed
)

// OwnerEvent is emitted by JoinAsRoomOwner to report state changes as they
// happen; the caller (e.g. a TUI) owns all presentation.
type OwnerEvent struct {
	Kind           OwnerEventKind
	RoomId         string
	GuestClientId  string
	GuestPublicKey []byte
	Accepted       bool
	Err            error
}

// OwnerEventFunc receives OwnerEvents. It must not block for long: it is
// called from the same goroutine that is servicing ADB stream traffic, so a
// slow handler stalls relaying for every guest.
type OwnerEventFunc func(OwnerEvent)

func emitOwner(onEvent OwnerEventFunc, event OwnerEvent) {
	if onEvent != nil {
		onEvent(event)
	}
}

// GuestEventKind identifies what happened during JoinAsGuest.
type GuestEventKind int

const (
	// GuestJoinDecided reports whether the room owner accepted the join
	// request. When Accepted, OwnerClientId and OwnerPublicKey identify the
	// owner (see client/identity) so the guest can display a fingerprint of
	// it.
	GuestJoinDecided GuestEventKind = iota
	// GuestProxyReady reports that the local AdbProxy is listening, with
	// the port a real "adb connect" should target.
	GuestProxyReady
	// GuestLocalAdbConnected reports that a local adb server connected to
	// the proxy and completed its handshake.
	GuestLocalAdbConnected
	// GuestRelayStopped reports that relaying for the current local
	// connection stopped (Err is the reason; nil only on graceful
	// shutdown). JoinAsGuest goes back to waiting for a new local
	// connection afterward, unless ctx was cancelled.
	GuestRelayStopped
	// GuestAdbConnected reports that JoinAsGuest ran "adb connect" against
	// the local proxy automatically (Err is nil).
	GuestAdbConnected
	// GuestAdbConnectFailed reports that the automatic "adb connect"
	// failed (Err is the reason); the operator can still run it manually.
	GuestAdbConnectFailed
)

// GuestEvent is emitted by JoinAsGuest to report state changes as they
// happen; the caller (e.g. a TUI) owns all presentation.
type GuestEvent struct {
	Kind           GuestEventKind
	Accepted       bool
	OwnerClientId  string
	OwnerPublicKey []byte
	LocalPort      string
	Err            error
}

// GuestEventFunc receives GuestEvents. It must not block for long, for the
// same reason as OwnerEventFunc.
type GuestEventFunc func(GuestEvent)

func emitGuest(onEvent GuestEventFunc, event GuestEvent) {
	if onEvent != nil {
		onEvent(event)
	}
}
