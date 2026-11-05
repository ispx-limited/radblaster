package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ispx-limited/radblaster/internal/engine"
	"github.com/ispx-limited/radblaster/internal/stats"
)

func TestCompact(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{9999, "9999"},
		{10_000, "10.0k"},
		{45_231, "45.2k"},
		{999_999, "1000.0k"},
		{1_000_000, "1.00M"},
		{12_345_678, "12.35M"},
	}
	for _, tc := range tests {
		if got := compact(tc.in); got != tc.want {
			t.Errorf("compact(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func snapshotFor(t *testing.T) engine.Snapshot {
	t.Helper()
	s := engine.Snapshot{Elapsed: 12 * time.Second, SetupRate: 3201, SessionsOnline: 4102, Outstanding: 512}
	for e := range s.Exchanges {
		s.Exchanges[e] = &engine.Metrics{
			Latency:     stats.NewHistogram(),
			ServiceTime: stats.NewHistogram(),
		}
	}
	s.Exchanges[engine.ExchangeAccess].Sent = 45231
	s.Exchanges[engine.ExchangeAccess].Latency.Record(uint64(4200 * time.Microsecond))
	return s
}

func TestProgressLine(t *testing.T) {
	got := progressLine(snapshotFor(t), true)

	// Counts below ten thousand stay exact, which is more useful than a
	// rounded figure at that size.
	for _, want := range []string{"12s", "auth 45.2k", "3201/s", "online 4102", "waiting 512", "p95"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress line %q is missing %q", got, want)
		}
	}
}

// TestProgressLineOmitsAccountingWhenThereIsNone keeps the line free of a
// figure that would always read zero in an authentication-only run.
func TestProgressLineOmitsAccountingWhenThereIsNone(t *testing.T) {
	got := progressLine(snapshotFor(t), false)

	if strings.Contains(got, "online") {
		t.Errorf("progress line %q reports sessions online with accounting disabled", got)
	}
}

// TestProgressLineShowsLossOnlyWhenThereIsSome keeps a healthy run's line
// quiet, so a number appearing is itself the signal.
func TestProgressLineShowsLossOnlyWhenThereIsSome(t *testing.T) {
	healthy := snapshotFor(t)
	if got := progressLine(healthy, true); strings.Contains(got, "lost") || strings.Contains(got, "retried") {
		t.Errorf("a healthy run reported loss: %q", got)
	}

	lossy := snapshotFor(t)
	lossy.Exchanges[engine.ExchangeAccess].Timeouts = 12
	lossy.Exchanges[engine.ExchangeAccess].Retransmits = 34
	got := progressLine(lossy, true)
	if !strings.Contains(got, "lost 12") {
		t.Errorf("progress line %q does not report the loss", got)
	}
	if !strings.Contains(got, "retried 34") {
		t.Errorf("progress line %q does not report the retransmissions", got)
	}
}
