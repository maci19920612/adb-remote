package capture

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestCapture(t *testing.T) (*Capture, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.pcapng")
	capture, err := Create(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Create failed: %s", err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	return capture, path
}

// dialCaptured opens a loopback TCP connection through a capturing
// listener and returns the client side and the (wrapped) server side.
func dialCaptured(t *testing.T, capture *Capture) (net.Conn, net.Conn) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	listener := capture.Listener(inner)
	t.Cleanup(func() { _ = listener.Close() })

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial: %s", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("failed to accept: %s", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func addrPortOf(t *testing.T, addr net.Addr) netip.AddrPort {
	t.Helper()
	return netip.MustParseAddrPort(addr.String())
}

func TestCreateWritesOnlyHeaderBlocks(t *testing.T) {
	capture, path := newTestCapture(t)
	if err := capture.Close(); err != nil {
		t.Fatalf("Close failed: %s", err)
	}

	file := readCaptureFile(t, path)

	if len(file.blockTypes) != 2 || file.blockTypes[0] != blockTypeSectionHeader || file.blockTypes[1] != blockTypeInterfaceDescription {
		t.Fatalf("expected a section header and an interface description block, got %#x", file.blockTypes)
	}
}

func TestCreateTruncatesAnExistingFileAndRestrictsItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.pcapng")
	if err := os.WriteFile(path, bytes.Repeat([]byte("old"), 100), 0o644); err != nil {
		t.Fatalf("failed to write the old capture: %s", err)
	}

	capture, err := Create(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Create failed: %s", err)
	}
	_ = capture.Close()

	if file := readCaptureFile(t, path); len(file.blockTypes) != 2 {
		t.Fatalf("expected only the fresh header blocks, got %#x", file.blockTypes)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat failed: %s", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected mode 0600 (the capture holds TLS secrets), got %o", info.Mode().Perm())
		}
	}
}

func TestCreateFailsForAnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "capture.pcapng")
	if _, err := Create(path, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatalf("expected an error for a path in a missing directory")
	}
}

func TestCapturedConnectionRecordsHandshakeDataAndClose(t *testing.T) {
	capture, path := newTestCapture(t)
	client, server := dialCaptured(t, capture)
	clientAddr, serverAddr := addrPortOf(t, client.LocalAddr()), addrPortOf(t, client.RemoteAddr())

	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatalf("client write failed: %s", err)
	}
	if _, err := io.ReadFull(server, make([]byte, 5)); err != nil {
		t.Fatalf("server read failed: %s", err)
	}
	if _, err := server.Write([]byte("world")); err != nil {
		t.Fatalf("server write failed: %s", err)
	}
	if _, err := io.ReadFull(client, make([]byte, 5)); err != nil {
		t.Fatalf("client read failed: %s", err)
	}
	_ = client.Close()
	if _, err := server.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("expected EOF on the server after the client closed, got %v", err)
	}
	_ = server.Close()
	_ = capture.Close()

	expected := []capturedSegment{
		{src: clientAddr, dst: serverAddr, seq: 0, ack: 0, flags: flagSYN},
		{src: serverAddr, dst: clientAddr, seq: 0, ack: 1, flags: flagSYN | flagACK},
		{src: clientAddr, dst: serverAddr, seq: 1, ack: 1, flags: flagACK},
		{src: clientAddr, dst: serverAddr, seq: 1, ack: 1, flags: flagPSH | flagACK, payload: []byte("hello")},
		{src: serverAddr, dst: clientAddr, seq: 1, ack: 6, flags: flagPSH | flagACK, payload: []byte("world")},
		{src: clientAddr, dst: serverAddr, seq: 6, ack: 6, flags: flagFIN | flagACK},
		{src: serverAddr, dst: clientAddr, seq: 6, ack: 7, flags: flagFIN | flagACK},
	}
	assertSegments(t, readCaptureFile(t, path).segments, expected)
}

func TestServerCloseRecordsASingleFin(t *testing.T) {
	capture, path := newTestCapture(t)
	_, server := dialCaptured(t, capture)

	_ = server.Close()
	_ = server.Close()
	_ = capture.Close()

	fins := 0
	for _, segment := range readCaptureFile(t, path).segments {
		if segment.flags&flagFIN != 0 {
			fins++
		}
	}
	if fins != 1 {
		t.Fatalf("expected exactly one FIN, got %d", fins)
	}
}

func TestLargeWriteIsSplitIntoConsecutiveSegments(t *testing.T) {
	capture, path := newTestCapture(t)
	client, server := dialCaptured(t, capture)
	data := bytes.Repeat([]byte{0xAB}, maxSegmentPayload+10)

	go func() { _, _ = io.ReadFull(client, make([]byte, len(data))) }()
	if _, err := server.Write(data); err != nil {
		t.Fatalf("server write failed: %s", err)
	}
	_ = capture.Close()

	var dataSegments []capturedSegment
	for _, segment := range readCaptureFile(t, path).segments {
		if len(segment.payload) > 0 {
			dataSegments = append(dataSegments, segment)
		}
	}
	if len(dataSegments) != 2 {
		t.Fatalf("expected 2 data segments, got %d", len(dataSegments))
	}
	if len(dataSegments[0].payload) != maxSegmentPayload || len(dataSegments[1].payload) != 10 {
		t.Fatalf("unexpected segment sizes %d and %d", len(dataSegments[0].payload), len(dataSegments[1].payload))
	}
	if dataSegments[1].seq != dataSegments[0].seq+maxSegmentPayload {
		t.Fatalf("expected consecutive sequence numbers, got %d then %d", dataSegments[0].seq, dataSegments[1].seq)
	}
}

func TestKeyLogWriterRecordsDecryptionSecrets(t *testing.T) {
	capture, path := newTestCapture(t)
	line := []byte("CLIENT_TRAFFIC_SECRET_0 aabb ccdd\n")

	n, err := capture.KeyLogWriter().Write(line)
	if n != len(line) || err != nil {
		t.Fatalf("expected a full write without error, got n=%d err=%v", n, err)
	}
	_ = capture.Close()

	secrets := readCaptureFile(t, path).secrets
	if len(secrets) != 1 || !bytes.Equal(secrets[0], line) {
		t.Fatalf("expected the key log line as one decryption secrets block, got %q", secrets)
	}
}

func TestWriteFailureStopsTheCaptureWithoutBreakingTraffic(t *testing.T) {
	var logs bytes.Buffer
	path := filepath.Join(t.TempDir(), "capture.pcapng")
	capture, err := Create(path, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("Create failed: %s", err)
	}
	client, server := dialCaptured(t, capture)
	// Simulate the disk going away under the capture.
	_ = capture.file.Close()

	if n, err := capture.KeyLogWriter().Write([]byte("CLIENT_RANDOM 00 11\n")); n != 20 || err != nil {
		t.Fatalf("expected the key log write to report success regardless, got n=%d err=%v", n, err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("client write failed: %s", err)
	}
	if _, err := io.ReadFull(server, make([]byte, 4)); err != nil {
		t.Fatalf("expected traffic to keep flowing after the capture failed, got %s", err)
	}
	if count := strings.Count(logs.String(), "Packet capture stopped"); count != 1 {
		t.Fatalf("expected the failure to be logged exactly once, got %d times:\n%s", count, logs.String())
	}
}

func TestRecordingAfterCloseIsANoop(t *testing.T) {
	capture, _ := newTestCapture(t)
	client, server := dialCaptured(t, capture)
	_ = capture.Close()

	if n, err := capture.KeyLogWriter().Write([]byte("x\n")); n != 2 || err != nil {
		t.Fatalf("expected the key log write to report success, got n=%d err=%v", n, err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("client write failed: %s", err)
	}
	if _, err := io.ReadFull(server, make([]byte, 4)); err != nil {
		t.Fatalf("server read failed: %s", err)
	}
}

func assertSegments(t *testing.T, actual []capturedSegment, expected []capturedSegment) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("expected %d segments, got %d: %+v", len(expected), len(actual), actual)
	}
	for i := range expected {
		a, e := actual[i], expected[i]
		if a.src != e.src || a.dst != e.dst || a.seq != e.seq || a.ack != e.ack || a.flags != e.flags || !bytes.Equal(a.payload, e.payload) {
			t.Fatalf("segment %d: expected %+v, got %+v", i, e, a)
		}
	}
}
