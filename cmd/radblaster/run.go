package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ispx-limited/radblaster/internal/engine"
	"github.com/ispx-limited/radblaster/internal/radius"
	"github.com/ispx-limited/radblaster/internal/stats"
)

// reportedQuantiles are the points a load result is summarised at. The tail
// matters more than the middle here: an AAA platform that is fine at the median
// and falls over at the 99th percentile is a platform that drops subscribers.
var reportedQuantiles = []float64{0.5, 0.9, 0.95, 0.99, 0.999}

func runLoad(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		server   = fs.String("server", "", "RADIUS server as host:port (required)")
		secret   = fs.String("secret", "", "shared secret, or set RADBLASTER_SECRET")
		user     = fs.String("user", "subscriber{n}@example.net", "User-Name template; {n} is the request number")
		password = fs.String("password", "", "password to authenticate with")
		chap     = fs.Bool("chap", false, "authenticate with CHAP instead of PAP")
		rate     = fs.Float64("rate", 0, "requests per second to offer; 0 offers as fast as possible")
		count    = fs.Uint64("count", 0, "stop after this many requests")
		duration = fs.Duration("duration", 0, "stop after this long")
		sockets  = fs.Int("sockets", 0, "source ports to spread across; 0 picks a default")
		timeout  = fs.Duration("timeout", 5*time.Second, "how long to wait for a reply")
		nasID    = fs.String("nas-id", "", "NAS-Identifier to send")
		seed     = fs.Uint64("seed", 0, "seed for a reproducible run; 0 draws a fresh one")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Offer sustained RADIUS load and report the result.\n\nFlags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nA run needs either -count or -duration. The shared secret is read from")
		fmt.Fprintln(os.Stderr, "RADBLASTER_SECRET if -secret is not given, which keeps it out of the")
		fmt.Fprintln(os.Stderr, "process list and the shell history.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *secret == "" {
		*secret = os.Getenv("RADBLASTER_SECRET")
	}
	switch {
	case *server == "":
		return errors.New("-server is required")
	case *secret == "":
		return errors.New("a shared secret is required, via -secret or RADBLASTER_SECRET")
	case *count == 0 && *duration == 0:
		return errors.New("a run needs either -count or -duration")
	}
	if !strings.Contains(*server, ":") {
		*server += ":1812"
	}

	auth := engine.AuthPAP
	if *chap {
		auth = engine.AuthCHAP
	}

	cfg := engine.Config{
		Server:        *server,
		Secret:        []byte(*secret),
		Rate:          *rate,
		Count:         *count,
		Duration:      *duration,
		Sockets:       *sockets,
		Timeout:       *timeout,
		Auth:          auth,
		Username:      *user,
		Password:      *password,
		NASIdentifier: *nasID,
		Seed:          *seed,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "Offering load to %s. Press Ctrl-C to stop.\n\n", *server)

	res, err := engine.Run(ctx, cfg)
	if err != nil {
		return err
	}

	report(os.Stdout, res)

	// A run where nothing was answered is a failed run, not a successful one
	// that happens to report zeros.
	if res.Access.Answered() == 0 && res.Access.Sent > 0 {
		return errors.New("no request was answered")
	}
	return nil
}

func report(w *os.File, res *engine.Result) {
	m := res.Access

	fmt.Fprintf(w, "Run finished in %s\n\n", res.Elapsed.Round(time.Millisecond))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "%s\n", m.Name)
	fmt.Fprintf(tw, "  Sent\t%d\n", m.Sent)
	fmt.Fprintf(tw, "  Accepted\t%d\n", m.Accepted)
	fmt.Fprintf(tw, "  Rejected\t%d\n", m.Rejected)
	if m.Challenged > 0 {
		fmt.Fprintf(tw, "  Challenged\t%d\n", m.Challenged)
	}
	fmt.Fprintf(tw, "  Timed out\t%d\n", m.Timeouts)
	if m.Unverified > 0 {
		fmt.Fprintf(tw, "  Failed verification\t%d\t(check the shared secret)\n", m.Unverified)
	}
	if m.Malformed > 0 {
		fmt.Fprintf(tw, "  Malformed\t%d\n", m.Malformed)
	}
	if m.Unmatched > 0 {
		fmt.Fprintf(tw, "  Unmatched\t%d\t(replies to requests already given up on)\n", m.Unmatched)
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Rate\n")
	if res.OfferedRate > 0 {
		fmt.Fprintf(tw, "  Offered\t%.0f/s\n", res.OfferedRate)
	}
	fmt.Fprintf(tw, "  Achieved\t%.0f/s\n", res.AchievedRate)
	_ = tw.Flush()

	if m.Latency.Count() > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "Latency\tfrom scheduled send\tfrom actual send\n")
		for _, q := range reportedQuantiles {
			fmt.Fprintf(tw, "  p%s\t%s\t%s\n",
				formatQuantile(q),
				fmtDur(m.Latency.Quantile(q)),
				fmtDur(m.ServiceTime.Quantile(q)))
		}
		fmt.Fprintf(tw, "  max\t%s\t%s\n", fmtDur(m.Latency.Max()), fmtDur(m.ServiceTime.Max()))
		fmt.Fprintf(tw, "  mean\t%s\t%s\n", fmtMean(m.Latency), fmtMean(m.ServiceTime))
		_ = tw.Flush()
	}

	warn(w, res)
}

// warn prints the things that make a result mean less than it appears to.
// Silence about them would be worse than a smaller number.
func warn(w *os.File, res *engine.Result) {
	m := res.Access
	var notes []string

	// A blocked request means every identifier on its socket was already
	// outstanding, but the reason matters and the remedy is opposite in the
	// two cases. If replies are coming back promptly we simply did not open
	// enough sockets. If they are timing out, identifiers are being held by
	// requests the server never answered, and opening more sockets would only
	// push harder at something already failing.
	lossy := m.Sent > 0 && float64(m.Timeouts)/float64(m.Sent) > 0.01

	switch {
	case m.Blocked > 0 && lossy:
		notes = append(notes, fmt.Sprintf(
			"%d requests could not be offered, but that is a symptom rather than the problem: "+
				"identifiers stayed in use because %d requests went unanswered. The server is the "+
				"limit here, so more sockets would only push harder at something already dropping.",
			m.Blocked, m.Timeouts))

	case m.Blocked > 0:
		// Concurrency-bound. The ceiling follows from how many requests can
		// be in flight at once and how long each one occupies its slot, so
		// state both rather than just advising more sockets.
		capacity := res.Sockets * radius.IDSpace
		note := fmt.Sprintf(
			"%d requests could not be offered: %d sockets carry at most %d requests at once.",
			m.Blocked, res.Sockets, capacity)
		if mean := m.ServiceTime.Mean(); mean > 0 {
			ceiling := float64(capacity) / (mean / float64(time.Second))
			note += fmt.Sprintf(
				" At a mean server response of %s that caps this run at about %.0f/s regardless of the rate asked for."+
					" Raise -sockets to lift the ceiling.",
				fmtDur(uint64(mean)), ceiling)
		} else {
			note += " Raise -sockets to lift the ceiling."
		}
		notes = append(notes, note)
	}

	if m.Timeouts > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d of %d requests (%.1f%%) were never answered.",
			m.Timeouts, m.Sent, 100*float64(m.Timeouts)/float64(m.Sent)))
	}
	if m.Unmatched > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d replies arrived for requests already given up on, so the timeout may be too short.",
			m.Unmatched))
	}
	if res.OfferedRate > 0 && res.AchievedRate < res.OfferedRate*0.95 {
		notes = append(notes, fmt.Sprintf(
			"Achieved %.0f/s against an offered %.0f/s, so the server was tested at less load than asked for.",
			res.AchievedRate, res.OfferedRate))
	}
	if m.Latency.Count() > 0 {
		scheduled := m.Latency.Quantile(0.5)
		service := m.ServiceTime.Quantile(0.5)
		// Only worth saying when the gap is large enough to change a
		// conclusion. Sub-millisecond slippage is timer granularity rather
		// than the generator struggling, and reporting it every run would
		// teach people to ignore the notes.
		const materialSlip = 2 * time.Millisecond
		slip := time.Duration(scheduled - service)
		if scheduled > service && slip > materialSlip && scheduled > service*2 {
			notes = append(notes, fmt.Sprintf(
				"Requests went out a median of %s later than scheduled, so the latency measured "+
					"from the scheduled send (%s) is mostly this run failing to keep pace rather "+
					"than the server being slow (%s from the actual send).",
				fmtDur(uint64(slip)), fmtDur(scheduled), fmtDur(service)))
		}
	}

	if len(notes) == 0 {
		return
	}
	fmt.Fprintln(w, "\nNotes")
	for _, n := range notes {
		fmt.Fprintf(w, "  - %s\n", n)
	}
}

func formatQuantile(q float64) string {
	s := fmt.Sprintf("%g", q*100)
	return s
}

func fmtDur(ns uint64) string {
	return time.Duration(ns).Round(time.Microsecond).String()
}

func fmtMean(h *stats.Histogram) string {
	return time.Duration(h.Mean()).Round(time.Microsecond).String()
}
