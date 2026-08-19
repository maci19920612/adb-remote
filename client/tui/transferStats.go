package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// transferStatsSource is the subset of transportLayer.Client the footer
// polls for cumulative wire byte counts.
type transferStatsSource interface {
	BytesSent() uint64
	BytesReceived() uint64
}

const transferStatsInterval = 500 * time.Millisecond

// transferStats tracks cumulative bytes transferred and the current
// transfer rate, sampled periodically from a transferStatsSource.
type transferStats struct {
	sent, received     uint64
	sendRate, recvRate float64 // bytes/sec, as of the most recent sample

	lastSampleAt           time.Time
	lastSent, lastReceived uint64
}

type transferTickMsg time.Time

// tickTransferStats starts (or continues) the periodic sampling that drives
// the footer's speed readout.
func tickTransferStats() tea.Cmd {
	return tea.Tick(transferStatsInterval, func(t time.Time) tea.Msg {
		return transferTickMsg(t)
	})
}

// sample updates the tracker from a fresh read of source and returns a
// command scheduling the next sample.
func (s *transferStats) sample(source transferStatsSource, now time.Time) tea.Cmd {
	sent := source.BytesSent()
	received := source.BytesReceived()

	if !s.lastSampleAt.IsZero() {
		if elapsed := now.Sub(s.lastSampleAt).Seconds(); elapsed > 0 {
			s.sendRate = float64(sent-s.lastSent) / elapsed
			s.recvRate = float64(received-s.lastReceived) / elapsed
		}
	}

	s.sent, s.received = sent, received
	s.lastSent, s.lastReceived = sent, received
	s.lastSampleAt = now

	return tickTransferStats()
}

// render produces the bottom-of-screen transfer stats line.
func (s *transferStats) render() string {
	return labelStyle.Render("In: ") + formatBytes(s.received) + dimStyle.Render(" ("+formatRate(s.recvRate)+")") +
		"   " +
		labelStyle.Render("Out: ") + formatBytes(s.sent) + dimStyle.Render(" ("+formatRate(s.sendRate)+")")
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for n/div >= unit && exp < 5 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func formatRate(bytesPerSec float64) string {
	return formatBytes(uint64(bytesPerSec)) + "/s"
}

// layoutWithFooter pads body with blank lines (or truncates it from the
// bottom, if it would otherwise overrun) so footer renders as the last
// line(s) of a height-row-tall screen — used to pin the transfer-stats
// footer to the bottom of a full-screen TUI regardless of how much body
// content precedes it. If height is unknown (0, before the first
// tea.WindowSizeMsg arrives), footer is just appended after body.
func layoutWithFooter(body string, footer string, height int) string {
	if height <= 0 {
		return body + "\n" + footer
	}

	bodyLines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	footerLines := strings.Split(footer, "\n")
	divider := dimStyle.Render(strings.Repeat("─", 40))

	available := height - len(footerLines) - 1 // 1 line reserved for the divider
	if available < 0 {
		available = 0
	}
	if len(bodyLines) > available {
		bodyLines = bodyLines[:available]
	}

	var b strings.Builder
	b.WriteString(strings.Join(bodyLines, "\n"))
	b.WriteString(strings.Repeat("\n", available-len(bodyLines)+1))
	b.WriteString(divider + "\n")
	b.WriteString(footer)
	return b.String()
}
