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
	"github.com/ispx-limited/radblaster/internal/profile"
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
		access   = fs.String("access", "pap", "access method: pap, chap or ipoe")

		rate     = fs.Float64("rate", 0, "sessions to set up per second; 0 goes as fast as possible")
		count    = fs.Uint64("count", 0, "stop after this many sessions")
		duration = fs.Duration("duration", 0, "stop after this long")
		hold     = fs.Duration("hold", 0, "how long each session stays online before it is torn down")
		interim  = fs.Duration("interim", 0, "interim update period when the server does not specify one")

		up      = fs.Uint64("traffic-up", 0, "octets per second a session reports as sent")
		upMax   = fs.Uint64("traffic-up-max", 0, "upper end of the upstream range; each session draws its own")
		down    = fs.Uint64("traffic-down", 0, "octets per second a session reports as received")
		downMax = fs.Uint64("traffic-down-max", 0, "upper end of the downstream range")

		shape       = fs.String("shape", "steady", "load shape: steady, ramp, burst or stampede")
		burstPeriod = fs.Duration("burst-period", 0, "one full cycle of the burst shape")
		burstLow    = fs.Float64("burst-low", 0, "fraction of the rate offered during the quiet half of a burst")
		rejectRate  = fs.Float64("reject-rate", 0, "fraction of sessions given a username the server will not know")

		links   = fs.Int("links", 0, "source ports per endpoint; 0 picks a default")
		timeout = fs.Duration("timeout", 5*time.Second, "how long to wait for a reply before trying again")
		retries = fs.Int("retries", 2, "times to repeat an unanswered request; 0 measures raw loss")

		nasID     = fs.String("nas-id", "", "NAS-Identifier to send")
		nasIP     = fs.String("nas-ip", "", "NAS-IP-Address to send")
		nasPortID = fs.String("nas-port-id", "", "NAS-Port-Id template; {n} is the session number")
		calling   = fs.String("calling-station-id", "", "Calling-Station-Id template; {n} is the session number")
		portType  = fs.Uint("nas-port-type", 0, "NAS-Port-Type value, for example 15 for Ethernet")

		coaListen = fs.String("coa-listen", "", "address to accept CoA and Disconnect on, for example :3799")
		allowNoMA = fs.Bool("allow-missing-message-authenticator", false,
			"accept replies that carry no Message-Authenticator, which CVE-2024-3596 makes forgeable")

		dicts stringList
		attrs stringList

		seed = fs.Uint64("seed", 0, "seed for a reproducible run; 0 draws a fresh one")
	)
	profilePath := fs.String("profile", "", "profile file to take settings from; flags override it")
	noAutoDict := fs.Bool("no-auto-dictionary", false, "do not look for dictionaries in the usual system locations")
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
		fmt.Fprintln(os.Stderr, "\nIPoE subscribers do not authenticate. With -access ipoe the password is")
		fmt.Fprintln(os.Stderr, "the fixed service string the access node sends so the request carries one,")
		fmt.Fprintln(os.Stderr, "the same for every subscriber, and the server identifies them by line.")
		fmt.Fprintln(os.Stderr, "\nradblaster ships definitions for the RFC attributes only. For vendor")
		fmt.Fprintln(os.Stderr, "attributes, point -dictionary at the dictionary files your own RADIUS")
		fmt.Fprintln(os.Stderr, "server already uses, so both ends resolve the same names to the same")
		fmt.Fprintln(os.Stderr, "numbers. Without a dictionary an attribute can still be written by")
		fmt.Fprintln(os.Stderr, "number, as in -attr '26.9.1=ip:qos-policy-in=voice'.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// A profile supplies the settings, then any flag given on the command line
	// overrides it, so one saved estate can be pointed at a different server
	// or run at a different rate without editing the file.
	if *profilePath != "" {
		p, err := profile.LoadFile(*profilePath)
		if err != nil {
			return err
		}
		applyProfile(fs, p, &profileOverrides{
			server: server, acct: acct, user: user, access: access,
			rate: rate, count: count, duration: duration, hold: hold, interim: interim,
			up: up, upMax: upMax, down: down, downMax: downMax,
			shape: shape, burstPeriod: burstPeriod, burstLow: burstLow, rejectRate: rejectRate,
			links: links, timeout: timeout, retries: retries,
			nasID: nasID, nasIP: nasIP, nasPortID: nasPortID, calling: calling, portType: portType,
			coaListen: coaListen, seed: seed, attrs: &attrs, dicts: &dicts,
		})
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

	dict, dictPaths, err := loadDictionaries(dicts, *noAutoDict)
	if err != nil {
		return err
	}
	reportDictionaries(dict, dictPaths)
	specs := make([]radius.AttrSpec, 0, len(attrs))
	for _, a := range attrs {
		spec, err := dict.ParseSpec(a)
		if err != nil {
			return err
		}
		specs = append(specs, spec)
	}

	var auth engine.Auth
	switch strings.ToLower(*access) {
	case "pap":
		auth = engine.AuthPAP
	case "chap":
		auth = engine.AuthCHAP
	case "ipoe":
		auth = engine.AuthIPoE
	default:
		return fmt.Errorf("unknown access method %q, want pap, chap or ipoe", *access)
	}

	var loadShape engine.Shape
	switch strings.ToLower(*shape) {
	case "steady":
		loadShape = engine.ShapeSteady
	case "ramp":
		loadShape = engine.ShapeRamp
	case "burst":
		loadShape = engine.ShapeBurst
	case "stampede":
		loadShape = engine.ShapeStampede
	default:
		return fmt.Errorf("unknown load shape %q, want steady, ramp, burst or stampede", *shape)
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
		TrafficUpMax:     *upMax,
		TrafficDown:      *down,
		TrafficDownMax:   *downMax,
		Shape:            loadShape,
		BurstPeriod:      *burstPeriod,
		BurstLow:         *burstLow,
		RejectRate:       *rejectRate,
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

		AllowMissingMessageAuthenticator: *allowNoMA,
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

	notes := res.Notes()
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
