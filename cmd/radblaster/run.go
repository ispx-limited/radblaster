package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ispx-limited/radblaster/internal/engine"
	"github.com/ispx-limited/radblaster/internal/radius"
	"github.com/ispx-limited/radblaster/internal/stats"
)

// reportedQuantiles are the points a result is summarised at. The tail matters
// more than the middle here: an AAA platform that is fine at the median and
// falls over at the 99th percentile is a platform that drops subscribers.
var reportedQuantiles = []float64{0.5, 0.9, 0.95, 0.99, 0.999}

func runLoad(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		server   = fs.String("server", "", "authentication server as host:port (required)")
		acct     = fs.String("acct-server", "", "accounting server as host:port; enables the full session lifecycle")
		secret   = fs.String("secret", "", "shared secret, or set RADBLASTER_SECRET")
		user     = fs.String("user", "subscriber{n}@example.net", "User-Name template; {n} is the session number")
		password = fs.String("password", "", "password to authenticate with")
		chap     = fs.Bool("chap", false, "authenticate with CHAP instead of PAP")

		rate     = fs.Float64("rate", 0, "sessions to set up per second; 0 goes as fast as possible")
		count    = fs.Uint64("count", 0, "stop after this many sessions")
		duration = fs.Duration("duration", 0, "stop after this long")
		hold     = fs.Duration("hold", 0, "how long each session stays online before it is torn down")
		interim  = fs.Duration("interim", 0, "interim update period when the server does not specify one")

		up   = fs.Uint64("traffic-up", 0, "octets per second each session reports as sent")
		down = fs.Uint64("traffic-down", 0, "octets per second each session reports as received")

		links   = fs.Int("links", 0, "source ports per endpoint; 0 picks a default")
		timeout = fs.Duration("timeout", 5*time.Second, "how long to wait for a reply before trying again")
		retries = fs.Int("retries", 2, "times to repeat an unanswered request; 0 measures raw loss")

		nasID     = fs.String("nas-id", "", "NAS-Identifier to send")
		nasIP     = fs.String("nas-ip", "", "NAS-IP-Address to send")
		nasPortID = fs.String("nas-port-id", "", "NAS-Port-Id template; {n} is the session number")
		calling   = fs.String("calling-station-id", "", "Calling-Station-Id template; {n} is the session number")
		portType  = fs.Uint("nas-port-type", 0, "NAS-Port-Type value, for example 15 for Ethernet")

		coaListen = fs.String("coa-listen", "", "address to accept CoA and Disconnect on, for example :3799")

		dicts stringList
		attrs stringList

		seed = fs.Uint64("seed", 0, "seed for a reproducible run; 0 draws a fresh one")
	)
	fs.Var(&dicts, "dictionary", "dictionary file or directory to load; repeatable")
	fs.Var(&attrs, "attr", "extra attribute as Name=value, repeatable; {n} is the session number")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Offer sustained RADIUS load and report the result.\n\nFlags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nA run needs either -count or -duration. Without -acct-server a session is")
		fmt.Fprintln(os.Stderr, "only an authentication; with it, each session also produces an")
		fmt.Fprintln(os.Stderr, "Accounting-Start, interim updates while it is online, and an")
		fmt.Fprintln(os.Stderr, "Accounting-Stop. The shared secret is read from RADBLASTER_SECRET if")
		fmt.Fprintln(os.Stderr, "-secret is not given, which keeps it out of the process list and the")
		fmt.Fprintln(os.Stderr, "shell history.")
		fmt.Fprintln(os.Stderr, "\nradblaster ships definitions for the RFC attributes only. For vendor")
		fmt.Fprintln(os.Stderr, "attributes, point -dictionary at the dictionary files your own RADIUS")
		fmt.Fprintln(os.Stderr, "server already uses, so both ends resolve the same names to the same")
		fmt.Fprintln(os.Stderr, "numbers. Without a dictionary an attribute can still be written by")
		fmt.Fprintln(os.Stderr, "number, as in -attr '26.9.1=ip:qos-policy-in=voice'.")
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
	if *acct != "" && !strings.Contains(*acct, ":") {
		*acct += ":1813"
	}

	dict := radius.StandardDictionary()
	if len(dicts) > 0 {
		var err error
		if dict, err = radius.LoadDictionary(dicts...); err != nil {
			return err
		}
	}
	specs := make([]radius.AttrSpec, 0, len(attrs))
	for _, a := range attrs {
		spec, err := dict.ParseSpec(a)
		if err != nil {
			return err
		}
		specs = append(specs, spec)
	}

	auth := engine.AuthPAP
	if *chap {
		auth = engine.AuthCHAP
	}

	var nasIPBytes []byte
	if *nasIP != "" {
		ip := net.ParseIP(*nasIP)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("-nas-ip %q is not an IPv4 address", *nasIP)
		}
		nasIPBytes = ip.To4()
	}

	cfg := engine.Config{
		Server:           *server,
		AcctServer:       *acct,
		Secret:           []byte(*secret),
		Rate:             *rate,
		Count:            *count,
		Duration:         *duration,
		Hold:             *hold,
		InterimInterval:  *interim,
		TrafficUp:        *up,
		TrafficDown:      *down,
		Links:            *links,
		Timeout:          *timeout,
		Retries:          *retries,
		Auth:             auth,
		Username:         *user,
		Password:         *password,
		NASIdentifier:    *nasID,
		NASIPAddress:     nasIPBytes,
		NASPortType:      uint32(*portType),
		CallingStationID: *calling,
		NASPortID:        *nasPortID,
		Attributes:       specs,
		CoAListen:        *coaListen,
		Seed:             *seed,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "Offering load to %s. Press Ctrl-C to stop.\n\n", *server)

	runner, err := engine.Start(ctx, cfg)
	if err != nil {
		return err
	}
	showProgress(os.Stderr, runner, cfg.AcctServer != "")

	res, err := runner.Wait()
	if err != nil {
		return err
	}

	report(os.Stdout, res)

	// A run where nothing was answered is a failed run, not a successful one
	// that happens to report zeros.
	if res.Access().Answered() == 0 && res.Access().Sent > 0 {
		return errors.New("no request was answered")
	}
	return nil
}

// stringList collects a flag that may be given more than once.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func report(w io.Writer, res *engine.Result) {
	fmt.Fprintf(w, "Run finished in %s\n\n", res.Elapsed.Round(time.Millisecond))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Exchange\tSent\tOK\tRejected\tRetried\tLost\tUnverified\tp50\tp95\tp99\n")
	for _, m := range res.Exchanges {
		if m.Sent == 0 {
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n",
			m.Name, m.Sent, m.Accepted, m.Rejected, m.Retransmits, m.Timeouts, m.Unverified,
			fmtDur(m.Latency.Quantile(0.5)),
			fmtDur(m.Latency.Quantile(0.95)),
			fmtDur(m.Latency.Quantile(0.99)))
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Sessions\n")
	fmt.Fprintf(tw, "  Started\t%d\n", res.SessionsStarted)
	if res.Accounting() {
		fmt.Fprintf(tw, "  Completed\t%d\n", res.SessionsCompleted)
	}
	if res.OfferedRate > 0 {
		fmt.Fprintf(tw, "  Setup rate offered\t%.0f/s\n", res.OfferedRate)
	}
	fmt.Fprintf(tw, "  Setup rate achieved\t%.0f/s\n", res.AchievedRate)
	_ = tw.Flush()

	if d := res.DynAuth; d.Total() > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "Server-initiated\tReceived\tAcknowledged\tRefused\n")
		if d.DisconnectRequests > 0 {
			fmt.Fprintf(tw, "  Disconnect-Request\t%d\t%d\t%d\n",
				d.DisconnectRequests, d.DisconnectACKs, d.DisconnectNAKs)
		}
		if d.CoARequests > 0 {
			fmt.Fprintf(tw, "  CoA-Request\t%d\t%d\t%d\n", d.CoARequests, d.CoAACKs, d.CoANAKs)
		}
		if d.Unverified > 0 {
			fmt.Fprintf(tw, "  Failed verification\t%d\t-\t-\n", d.Unverified)
		}
		if d.Malformed > 0 {
			fmt.Fprintf(tw, "  Malformed\t%d\t-\t-\n", d.Malformed)
		}
		_ = tw.Flush()
	}

	// Latency detail for authentication, which is the exchange most runs are
	// really asking about.
	m := res.Access()
	if m.Latency.Count() > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "%s latency\tfrom scheduled send\tfrom actual send\n", m.Name)
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
func warn(w io.Writer, res *engine.Result) {
	m := res.Access()
	var notes []string

	// A blocked request means every identifier on its link was already
	// outstanding, but the reason matters and the remedy is opposite in the
	// two cases. If replies are coming back we simply did not open enough
	// links. If they are timing out, identifiers are being held by requests
	// the server never answered, and opening more links would only push
	// harder at something already failing.
	var blocked, timeouts, sent uint64
	for _, e := range res.Exchanges {
		blocked += e.Blocked
		timeouts += e.Timeouts
		sent += e.Sent
	}
	lossy := sent > 0 && float64(timeouts)/float64(sent) > 0.01

	switch {
	case blocked > 0 && lossy:
		notes = append(notes, fmt.Sprintf(
			"%d requests could not be offered, but that is a symptom rather than the problem: "+
				"identifiers stayed in use because %d requests went unanswered. The server is the "+
				"limit here, so more links would only push harder at something already dropping.",
			blocked, timeouts))

	case blocked > 0:
		// Concurrency-bound. The ceiling follows from how many requests can
		// be in flight at once and how long each one occupies its slot, so
		// state both rather than just advising more links.
		capacity := res.Links * radius.IDSpace
		note := fmt.Sprintf(
			"%d requests could not be offered: %d links carry at most %d requests at once.",
			blocked, res.Links, capacity)
		if mean := m.ServiceTime.Mean(); mean > 0 {
			ceiling := float64(capacity) / (mean / float64(time.Second))
			note += fmt.Sprintf(
				" At a mean server response of %s that caps this run at about %.0f/s regardless of the rate asked for."+
					" Raise -links to lift the ceiling.",
				fmtDur(uint64(mean)), ceiling)
		} else {
			note += " Raise -links to lift the ceiling."
		}
		notes = append(notes, note)
	}

	for _, e := range res.Exchanges {
		if e.Retransmits > 0 && e.Sent > 0 {
			notes = append(notes, fmt.Sprintf(
				"%s needed %d retransmissions across %d requests. Retries hide loss in the success "+
					"rate, so this is the figure that shows the path or the server dropping packets.",
				e.Name, e.Retransmits, e.Sent))
		}
		if e.Timeouts > 0 {
			notes = append(notes, fmt.Sprintf(
				"%d of %d %s requests (%.1f%%) were never answered.",
				e.Timeouts, e.Sent, e.Name, 100*float64(e.Timeouts)/float64(e.Sent)))
		}
		if e.Unverified > 0 {
			notes = append(notes, fmt.Sprintf(
				"%d %s replies failed verification, which almost always means the shared secret is wrong.",
				e.Unverified, e.Name))
		}
	}

	if d := res.DynAuth; d.DisconnectNAKs+d.CoANAKs > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d server-initiated requests were refused because the session they named was no longer "+
				"online. For a bulk change that is the figure that matters: those subscribers did not "+
				"receive it.", d.DisconnectNAKs+d.CoANAKs))
	}
	if res.DynAuth.Unverified > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d server-initiated requests failed verification and were dropped without a reply, "+
				"which means they were signed with a different shared secret.", res.DynAuth.Unverified))
	}

	if res.OfferedRate > 0 && res.AchievedRate < res.OfferedRate*0.95 {
		notes = append(notes, fmt.Sprintf(
			"Achieved %.0f sessions/s against an offered %.0f/s, so the server was tested at less load than asked for.",
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
	return fmt.Sprintf("%g", q*100)
}

func fmtDur(ns uint64) string {
	return time.Duration(ns).Round(time.Microsecond).String()
}

func fmtMean(h *stats.Histogram) string {
	return time.Duration(h.Mean()).Round(time.Microsecond).String()
}
