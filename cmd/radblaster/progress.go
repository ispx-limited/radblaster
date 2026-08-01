package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ispx-limited/radblaster/internal/engine"
)

// progressInterval is how often the live line is redrawn. Fast enough to see a
// server start struggling, slow enough that the numbers are readable.
const progressInterval = 250 * time.Millisecond

// showProgress redraws a single status line while the run is going.
//
// It writes to stderr so the final report on stdout stays clean and
// machine-readable, and it only draws when stderr is a terminal: the line
// overwrites itself with a carriage return, which would turn a redirected log
// into a mess.
func showProgress(w *os.File, r *engine.Runner, accounting bool) {
	if !isTerminal(w) {
		return
	}

	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()

	width := 0
	for {
		select {
		case <-r.Done():
			// Wipe the line so the report starts on a clean one.
			fmt.Fprintf(w, "\r%s\r", strings.Repeat(" ", width))
			return
		case <-ticker.C:
			line := progressLine(r.Snapshot(), accounting)
			if len(line) < width {
				line += strings.Repeat(" ", width-len(line))
			}
			width = len(line)
			fmt.Fprintf(w, "\r%s", line)
		}
	}
}

func progressLine(s engine.Snapshot, accounting bool) string {
	m := s.Access()

	var b strings.Builder
	fmt.Fprintf(&b, "  %6s", s.Elapsed.Round(time.Second))
	fmt.Fprintf(&b, "  auth %s at %s/s", compact(m.Sent), compact(uint64(s.SetupRate+0.5)))

	if accounting {
		fmt.Fprintf(&b, "  online %s", compact(s.SessionsOnline))
	}

	// Outstanding moves before any latency figure does, so it is the earliest
	// sign of a server falling behind.
	fmt.Fprintf(&b, "  waiting %d", s.Outstanding)

	if m.Latency.Count() > 0 {
		fmt.Fprintf(&b, "  p95 %s", fmtDur(m.Latency.Quantile(0.95)))
	}

	var lost, retried uint64
	for _, e := range s.Exchanges {
		lost += e.Timeouts
		retried += e.Retransmits
	}
	if retried > 0 {
		fmt.Fprintf(&b, "  retried %s", compact(retried))
	}
	if lost > 0 {
		fmt.Fprintf(&b, "  lost %s", compact(lost))
	}
	return b.String()
}

// compact shortens large counts so the line stays a stable width as a run
// climbs into the millions.
func compact(n uint64) string {
	switch {
	case n < 10_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
}

// isTerminal reports whether w is a character device, which is the cheapest
// way to tell an interactive terminal from a redirected file without taking a
// dependency for it.
func isTerminal(w *os.File) bool {
	info, err := w.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
