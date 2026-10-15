package relay

import (
	"fmt"
	"strings"
)

// deviceServices are the services a guest may open: the ones adbd serves
// itself, and the socket specs "adb forward" connects to on the device.
// The owner asks its adb-server for them right after selecting the shared
// device, on the same socket, and adb-server still interprets some
// requests at that point instead of passing them to the device: host
// requests ("host:kill", "host:connect:<address>", "host-serial:<other
// device>:...") would let the guest drive the owner's adb-server and every
// other device on it. Allowing only device services keeps the guest on the
// one shared device.
var deviceServices = map[string]bool{
	// adbd's own services.
	"shell":           true,
	"exec":            true,
	"sync":            true,
	"abb":             true,
	"abb_exec":        true,
	"framebuffer":     true,
	"jdwp":            true,
	"track-jdwp":      true,
	"track-app":       true,
	"remount":         true,
	"reboot":          true,
	"root":            true,
	"unroot":          true,
	"backup":          true,
	"restore":         true,
	"disable-verity":  true,
	"enable-verity":   true,
	"tcpip":           true,
	"usb":             true,
	"dev":             true,
	"dev-raw":         true,
	"sink":            true,
	"source":          true,
	"spin":            true,
	"reconnect":       true,
	"sideload":        true,
	"sideload-host":   true,
	"rescue-install":  true,
	"rescue-getprop":  true,
	"rescue-wipe":     true,
	"reverse":         true, // see checkReverseService
	"tcp":             true,
	"local":           true,
	"localreserved":   true,
	"localabstract":   true,
	"localfilesystem": true,
	"vsock":           true,
}

// checkGuestService reports an error unless service is a device service a
// guest may open (see deviceServices).
func checkGuestService(service string) error {
	name := service
	if end := strings.IndexAny(service, ":,"); end >= 0 {
		name = service[:end]
	}
	if !deviceServices[name] {
		return fmt.Errorf("%q is not a device service", service)
	}
	if name == "reverse" {
		return checkReverseService(service)
	}
	return nil
}

// checkReverseService allows only the well-formed reverse requests:
// adb-server snoops every "reverse:" request passing through it to track
// reverse forwards, and aborts on one it doesn't recognise.
func checkReverseService(service string) error {
	switch {
	case service == reverseListForward, service == reverseKillForwardAll:
		return nil
	case strings.HasPrefix(service, reverseKillForwardPrefix):
		remote := strings.TrimPrefix(service, reverseKillForwardPrefix)
		if remote != "" && !strings.Contains(remote, ";") {
			return nil
		}
	case strings.HasPrefix(service, reverseForwardPrefix):
		spec := strings.TrimPrefix(strings.TrimPrefix(service, reverseForwardPrefix), reverseNoRebindPrefix)
		remote, local, ok := strings.Cut(spec, ";")
		if ok && remote != "" && local != "" {
			return nil
		}
	}
	return fmt.Errorf("%q is not a well-formed reverse request", service)
}
