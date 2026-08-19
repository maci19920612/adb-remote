package prettyLogHandler

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// TestWritesToGivenWriterNotStdout is the key regression test: a caller
// (e.g. a full-screen terminal UI) that redirects logging away from stdout
// must never see log output land on stdout regardless.
func TestWritesToGivenWriterNotStdout(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(CreatePrettyHandler(buffer, &slog.HandlerOptions{}))

	logger.Info("hello from the test", "key", "value")

	output := buffer.String()
	if !strings.Contains(output, "hello from the test") {
		t.Fatalf("expected the message in the buffer, got %q", output)
	}
	if !strings.Contains(output, "value") {
		t.Fatalf("expected the attribute in the buffer, got %q", output)
	}
}

func TestNilWriterDefaultsWithoutPanicking(t *testing.T) {
	// Exercises the os.Stdout fallback without actually asserting on
	// stdout content; the point is that it must not panic.
	handler := CreatePrettyHandler(nil, &slog.HandlerOptions{})
	logger := slog.New(handler)
	logger.Info("should not panic")
}

func TestRespectsLevelFiltering(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(CreatePrettyHandler(buffer, &slog.HandlerOptions{Level: slog.LevelWarn}))

	logger.Info("this should be filtered out")
	logger.Warn("this should appear")

	output := buffer.String()
	if strings.Contains(output, "filtered out") {
		t.Fatalf("expected info-level messages to be suppressed, got %q", output)
	}
	if !strings.Contains(output, "this should appear") {
		t.Fatalf("expected the warn-level message to appear, got %q", output)
	}
}

func TestWithAttrsPersistsWriter(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(CreatePrettyHandler(buffer, &slog.HandlerOptions{})).With("component", "test")

	logger.Info("message with attrs")

	output := buffer.String()
	if !strings.Contains(output, "component") {
		t.Fatalf("expected the persisted attribute to appear, got %q", output)
	}
}

// TestConcurrentLoggingDoesNotRace exercises the handler with a plain,
// unsynchronized bytes.Buffer as the writer — the handler holds its mutex
// across the whole Handle call (scratch buffer *and* the final write), so
// concurrent Info/Error calls from many goroutines (the normal case for the
// shared client.Logger in this app) must not race even when the writer
// itself provides no concurrency safety of its own.
func TestConcurrentLoggingDoesNotRace(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(CreatePrettyHandler(buffer, &slog.HandlerOptions{}))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			logger.Info("concurrent message", "i", i)
		}(i)
	}
	wg.Wait()
}
