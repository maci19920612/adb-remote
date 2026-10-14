// Package capture records every byte of every connection the transporter
// accepts into a pcapng file, for debug verbosity.
//
// Recording happens below TLS: the capturing listener wraps the raw TCP
// connection, and the TLS server is layered on top of it. So the file holds
// exactly what crossed the socket — TLS handshakes that succeeded or
// failed, and bytes from clients that never spoke TLS at all — framed as
// synthetic TCP segments between the real client and transporter
// addresses, which Wireshark reassembles into one stream per connection.
// The TLS session secrets are written into the same file as Decryption
// Secrets Blocks (see KeyLogWriter), so Wireshark also decrypts the
// successful sessions without any extra setup.
//
// The capture therefore contains everything relayed through the
// transporter, decryptable by anyone holding the file; it is created with
// mode 0600 and meant for debugging only.
package capture

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

// Capture is a pcapng file shared by every connection accepted through
// Listener. Recording never fails a caller: the first write error is
// logged and stops the capture, while the traffic itself carries on.
type Capture struct {
	mutex  sync.Mutex
	file   *os.File // nil once closed or after the first write failure
	logger *slog.Logger
}

// Create truncates (or creates) the capture file at path and writes the
// pcapng header blocks to it.
func Create(path string, logger *slog.Logger) (*Capture, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to create the packet capture file: %w", err)
	}
	// The mode passed to OpenFile only applies to a newly created file; an
	// existing one keeps its own, so restrict it explicitly.
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("failed to restrict the packet capture file mode: %w", err)
	}
	if _, err := file.Write(append(sectionHeaderBlock(), interfaceDescriptionBlock()...)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("failed to write the packet capture header: %w", err)
	}
	return &Capture{file: file, logger: logger}, nil
}

// Listener wraps inner so every connection it accepts is recorded.
func (c *Capture) Listener(inner net.Listener) net.Listener {
	return &listener{Listener: inner, capture: c}
}

// KeyLogWriter returns a writer for tls.Config.KeyLogWriter that records
// each TLS key log line as a Decryption Secrets Block.
func (c *Capture) KeyLogWriter() io.Writer {
	return keyLogWriter{capture: c}
}

// Close closes the capture file; anything recorded afterwards is dropped.
func (c *Capture) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.file == nil {
		return nil
	}
	err := c.file.Close()
	c.file = nil
	return err
}

func (c *Capture) writePacketLocked(ts time.Time, s segment) {
	if c.file == nil {
		return
	}
	c.writeBlockLocked(enhancedPacketBlock(ts, buildPacket(s)))
}

func (c *Capture) writeBlockLocked(block []byte) {
	if c.file == nil {
		return
	}
	if _, err := c.file.Write(block); err != nil {
		c.logger.Error(fmt.Sprintf("Packet capture stopped, writing %s failed: %s", c.file.Name(), err))
		_ = c.file.Close()
		c.file = nil
	}
}

type listener struct {
	net.Listener
	capture *Capture
}

func (l *listener) Accept() (net.Conn, error) {
	inner, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.capture.wrap(inner), nil
}

type keyLogWriter struct {
	capture *Capture
}

// Write never reports an error: crypto/tls aborts the handshake when its
// KeyLogWriter fails, and a broken capture must not take connections down
// with it.
func (w keyLogWriter) Write(line []byte) (int, error) {
	w.capture.mutex.Lock()
	defer w.capture.mutex.Unlock()
	w.capture.writeBlockLocked(decryptionSecretsBlock(line))
	return len(line), nil
}

// conn records every Read and Write on the raw connection as TCP segments,
// tracking each side's sequence number the way a real TCP stream would.
type conn struct {
	net.Conn
	capture        *Capture
	client, server netip.AddrPort

	// Guarded by capture.mutex.
	clientSeq, serverSeq           uint32
	clientFinished, serverFinished bool
}

func (c *Capture) wrap(inner net.Conn) *conn {
	client, server := endpointPair(inner.RemoteAddr(), inner.LocalAddr())
	wrapped := &conn{Conn: inner, capture: c, client: client, server: server}
	now := time.Now()

	c.mutex.Lock()
	defer c.mutex.Unlock()
	// The real three-way handshake completed in the kernel before Accept
	// returned. It is re-created so every connection shows up as a complete
	// TCP stream, including one that closes without sending a single byte.
	c.writePacketLocked(now, wrapped.nextSegmentLocked(true, flagSYN, nil))
	c.writePacketLocked(now, wrapped.nextSegmentLocked(false, flagSYN|flagACK, nil))
	c.writePacketLocked(now, wrapped.nextSegmentLocked(true, flagACK, nil))
	return wrapped
}

func (cc *conn) Read(b []byte) (int, error) {
	n, err := cc.Conn.Read(b)
	if n > 0 {
		cc.recordData(true, b[:n])
	}
	if errors.Is(err, io.EOF) {
		cc.recordFin(true)
	}
	return n, err
}

func (cc *conn) Write(b []byte) (int, error) {
	n, err := cc.Conn.Write(b)
	if n > 0 {
		cc.recordData(false, b[:n])
	}
	return n, err
}

func (cc *conn) Close() error {
	err := cc.Conn.Close()
	cc.recordFin(false)
	return err
}

func (cc *conn) recordData(fromClient bool, data []byte) {
	now := time.Now()
	cc.capture.mutex.Lock()
	defer cc.capture.mutex.Unlock()
	for len(data) > 0 {
		chunk := data[:min(len(data), maxSegmentPayload)]
		cc.capture.writePacketLocked(now, cc.nextSegmentLocked(fromClient, flagPSH|flagACK, chunk))
		data = data[len(chunk):]
	}
}

func (cc *conn) recordFin(fromClient bool) {
	now := time.Now()
	cc.capture.mutex.Lock()
	defer cc.capture.mutex.Unlock()
	finished := &cc.serverFinished
	if fromClient {
		finished = &cc.clientFinished
	}
	if *finished {
		return
	}
	*finished = true
	cc.capture.writePacketLocked(now, cc.nextSegmentLocked(fromClient, flagFIN|flagACK, nil))
}

// nextSegmentLocked builds the next segment one side sends, acknowledging
// everything the other side has sent so far, and advances the sender's
// sequence number past it (SYN and FIN each count as one).
func (cc *conn) nextSegmentLocked(fromClient bool, flags uint8, payload []byte) segment {
	length := uint32(len(payload))
	if flags&(flagSYN|flagFIN) != 0 {
		length++
	}
	if fromClient {
		s := segment{src: cc.client, dst: cc.server, seq: cc.clientSeq, flags: flags, payload: payload}
		if flags&flagACK != 0 {
			s.ack = cc.serverSeq
		}
		cc.clientSeq += length
		return s
	}
	s := segment{src: cc.server, dst: cc.client, seq: cc.serverSeq, flags: flags, payload: payload}
	if flags&flagACK != 0 {
		s.ack = cc.clientSeq
	}
	cc.serverSeq += length
	return s
}
