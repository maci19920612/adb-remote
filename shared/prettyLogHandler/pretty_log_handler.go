package prettyLogHandler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
)

const (
	reset = "\033[0m"

	black        = 30
	red          = 31
	green        = 32
	yellow       = 33
	blue         = 34
	magenta      = 35
	cyan         = 36
	lightGray    = 37
	darkGray     = 90
	lightRed     = 91
	lightGreen   = 92
	lightYellow  = 93
	lightBlue    = 94
	lightMagenta = 95
	lightCyan    = 96
	white        = 97
)

func colorize(colorCode int, v string) string {
	return fmt.Sprintf("\033[%sm%s%s", strconv.Itoa(colorCode), v, reset)
}

// CreatePrettyHandler builds a colorized, human-readable slog.Handler that
// writes each record to writer. Pass os.Stdout for a normal CLI process; a
// process that also renders a full-screen terminal UI on stdout (see
// client/tui) must redirect this elsewhere (e.g. a log file), since a log
// line writing straight to stdout out-of-band would corrupt the TUI's
// rendering.
func CreatePrettyHandler(writer io.Writer, opts *slog.HandlerOptions) *Handler {
	if writer == nil {
		writer = os.Stdout
	}
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	b := &bytes.Buffer{}
	return &Handler{
		writer: writer,
		buffer: b,
		slogHandler: slog.NewJSONHandler(b, &slog.HandlerOptions{
			Level:       opts.Level,
			AddSource:   opts.AddSource,
			ReplaceAttr: suppressDefaults(opts.ReplaceAttr),
		}),
		mutex: &sync.Mutex{},
	}
}

// computeAttrs must be called with h.mutex already held.
func (h *Handler) computeAttrs(
	ctx context.Context,
	r slog.Record,
) (map[string]any, error) {
	defer h.buffer.Reset()
	if err := h.slogHandler.Handle(ctx, r); err != nil {
		return nil, fmt.Errorf("error when calling inner handler's Handle: %w", err)
	}

	var attrs map[string]any
	err := json.Unmarshal(h.buffer.Bytes(), &attrs)
	if err != nil {
		return nil, fmt.Errorf("error when unmarshaling inner handler's Handle result: %w", err)
	}
	return attrs, nil
}

func suppressDefaults(
	next func([]string, slog.Attr) slog.Attr,
) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey ||
			a.Key == slog.LevelKey ||
			a.Key == slog.MessageKey {
			return slog.Attr{}
		}
		if next == nil {
			return a
		}
		return next(groups, a)
	}
}

type Handler struct {
	writer      io.Writer
	slogHandler slog.Handler
	buffer      *bytes.Buffer
	mutex       *sync.Mutex
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.slogHandler.Enabled(ctx, level)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		writer:      h.writer,
		slogHandler: h.slogHandler.WithAttrs(attrs),
		buffer:      h.buffer,
		mutex:       h.mutex,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		writer:      h.writer,
		slogHandler: h.slogHandler.WithGroup(name),
		buffer:      h.buffer,
		mutex:       h.mutex,
	}
}

const (
	timeFormat = "[15:04:05.000]"
)

// Handle holds h.mutex for its whole body — not just around the shared
// scratch buffer — so the final write to h.writer is serialized too. That
// keeps concurrent log calls from interleaving their output even when the
// writer itself isn't safe for concurrent Write calls on its own (unlike
// os.Stdout/os.File, which are).
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	level := r.Level.String() + ":"

	switch r.Level {
	case slog.LevelDebug:
		level = colorize(darkGray, level)
	case slog.LevelInfo:
		level = colorize(cyan, level)
	case slog.LevelWarn:
		level = colorize(lightYellow, level)
	case slog.LevelError:
		level = colorize(lightRed, level)
	}

	attrs, err := h.computeAttrs(ctx, r)
	if err != nil {
		return err
	}

	bytes, err := json.MarshalIndent(attrs, "", "  ")
	if err != nil {
		return fmt.Errorf("error when marshaling attrs: %w", err)
	}

	fmt.Fprintln(h.writer,
		colorize(lightGray, r.Time.Format(timeFormat)),
		level,
		colorize(white, r.Message),
		colorize(darkGray, string(bytes)),
	)

	return nil
}
