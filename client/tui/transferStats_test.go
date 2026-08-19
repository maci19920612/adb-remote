package tui

import (
	"strings"
	"testing"
	"time"
)

type fakeStatsSource struct {
	sent, received uint64
}

func (f *fakeStatsSource) BytesSent() uint64     { return f.sent }
func (f *fakeStatsSource) BytesReceived() uint64 { return f.received }

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{
		0:         "0 B",
		512:       "512 B",
		1024:      "1.0 KiB",
		1536:      "1.5 KiB",
		1 << 20:   "1.0 MiB",
		1<<20 + 1: "1.0 MiB",
	}
	for input, want := range cases {
		if got := formatBytes(input); got != want {
			t.Fatalf("formatBytes(%d): expected %q, got %q", input, want, got)
		}
	}
}

func TestTransferStatsSampleComputesRate(t *testing.T) {
	var s transferStats
	source := &fakeStatsSource{}
	start := time.Unix(1000, 0)

	// First sample only establishes the baseline; no elapsed time yet to
	// compute a rate from.
	s.sample(source, start)
	if s.sendRate != 0 || s.recvRate != 0 {
		t.Fatalf("expected zero rates on the first sample, got send=%v recv=%v", s.sendRate, s.recvRate)
	}

	source.sent = 2000
	source.received = 1000
	s.sample(source, start.Add(1*time.Second))

	if s.sendRate != 2000 {
		t.Fatalf("expected a send rate of 2000 B/s, got %v", s.sendRate)
	}
	if s.recvRate != 1000 {
		t.Fatalf("expected a receive rate of 1000 B/s, got %v", s.recvRate)
	}
	if s.sent != 2000 || s.received != 1000 {
		t.Fatalf("expected cumulative totals to track the source, got sent=%d received=%d", s.sent, s.received)
	}
}

func TestTransferStatsSampleReturnsRecurringCommand(t *testing.T) {
	var s transferStats
	cmd := s.sample(&fakeStatsSource{}, time.Unix(0, 0))
	if cmd == nil {
		t.Fatalf("expected sample to return a command scheduling the next tick")
	}
	if _, ok := cmd().(transferTickMsg); !ok {
		t.Fatalf("expected the command to eventually produce a transferTickMsg")
	}
}

func TestLayoutWithFooterPinsFooterToBottom(t *testing.T) {
	body := "line1\nline2"
	footer := "FOOTER"
	out := layoutWithFooter(body, footer, 10)

	lines := strings.Split(out, "\n")
	if lines[len(lines)-1] != footer {
		t.Fatalf("expected the last line to be the footer, got %q (full output: %q)", lines[len(lines)-1], out)
	}
	if len(lines) != 10 {
		t.Fatalf("expected exactly %d lines to fill the screen height, got %d: %q", 10, len(lines), lines)
	}
	if lines[0] != "line1" || lines[1] != "line2" {
		t.Fatalf("expected the body to appear first, got %q", lines[:2])
	}
}

func TestLayoutWithFooterTruncatesOverflowingBody(t *testing.T) {
	body := strings.Repeat("x\n", 20)
	footer := "FOOTER"
	out := layoutWithFooter(body, footer, 5)

	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected the output clamped to the screen height (5 lines), got %d", len(lines))
	}
	if lines[len(lines)-1] != footer {
		t.Fatalf("expected the footer to still be the last line after truncation, got %q", lines[len(lines)-1])
	}
}

func TestLayoutWithFooterUnknownHeightJustAppends(t *testing.T) {
	out := layoutWithFooter("body", "FOOTER", 0)
	if out != "body\nFOOTER" {
		t.Fatalf("expected a simple append when height is unknown, got %q", out)
	}
}
