// Package engine drives RADIUS load against a server and measures the result.
package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
	"github.com/ispx-limited/radblaster/internal/stats"
)

// extraAttr is a caller-supplied attribute ready to be added to a packet.
//
// A value with no template is encoded once at startup rather than for every
// packet, which keeps a long list of static vendor attributes off the hot
// path.
type extraAttr struct {
	spec   radius.AttrSpec
	static []byte
}

// Auth selects how a subscriber proves its identity.
type Auth string

// Supported authentication methods. Broadband access uses these two; a server
// that stores hashed passwords can only support PAP, because CHAP requires the
// server to hold the cleartext.
const (
	AuthPAP  Auth = "pap"
	AuthCHAP Auth = "chap"
)

// Config describes a run.
type Config struct {
	// Server is the authentication endpoint, as host:port.
	Server string
	// AcctServer is the accounting endpoint. Empty disables accounting, so a
	// session is then just an authentication.
	AcctServer string
	// Secret is the shared secret.
	Secret []byte

	// Rate is the offered session setup rate per second across the whole
	// run. Zero sets sessions up as fast as the links allow.
	Rate float64
	// Count bounds the run by number of sessions. Zero means unbounded.
	Count uint64
	// Duration bounds the run by wall-clock time. Zero means unbounded.
	Duration time.Duration

	// Hold is how long a session stays online before it is torn down. Zero
	// stops it as soon as accounting has started, which is the shape of a
	// churn test rather than a soak.
	Hold time.Duration
	// InterimInterval is the fallback update period, used when the server
	// does not state one in its Access-Accept.
	InterimInterval time.Duration

	// TrafficUp and TrafficDown are the octets per second each session
	// claims to have carried, which drives the accounting counters.
	TrafficUp   uint64
	TrafficDown uint64

	// Links is the number of source ports to spread across, per endpoint.
	// Each carries its own identifier space, so this sets the ceiling on
	// outstanding requests at 256 per link. Zero picks a default.
	Links int
	// Timeout is how long a request may go unanswered before it is lost.
	Timeout time.Duration

	Auth     Auth
	Username string
	Password string

	// Attributes a real access node would set.
	NASIdentifier    string
	NASIPAddress     []byte
	NASPortType      uint32
	CallingStationID string
	NASPortID        string
	TerminateCause   uint32

	// Attributes are extra attributes put on every request this run sends,
	// which is how vendor-specific attributes get onto the wire. Values may
	// contain {n}, substituted with the session number.
	Attributes []radius.AttrSpec

	// prepared holds Attributes with their values already encoded where they
	// are constant, so the send path only renders the ones that vary.
	prepared []extraAttr

	// CoAListen is the address to accept CoA-Request and Disconnect-Request
	// on, for example ":3799". Empty means the run does not present itself as
	// an access node the server can push changes to.
	CoAListen string

	// Seed makes a run reproducible. Zero draws a fresh seed.
	Seed uint64
}

func (c *Config) links() int {
	if c.Links > 0 {
		return c.Links
	}
	n := runtime.NumCPU()
	if n > 16 {
		n = 16
	}
	if n < 1 {
		n = 1
	}
	return n
}

func (c *Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 5 * time.Second
	}
	return c.Timeout
}

func (c *Config) accounting() bool { return c.AcctServer != "" }

func (c *Config) terminateCause() uint32 {
	if c.TerminateCause > 0 {
		return c.TerminateCause
	}
	return causeUserRequest
}

func (c *Config) validate() error {
	switch {
	case c.Server == "":
		return errors.New("engine: no server given")
	case len(c.Secret) == 0:
		return errors.New("engine: no shared secret given")
	case c.Username == "":
		return errors.New("engine: no username given")
	case c.Count == 0 && c.Duration == 0:
		return errors.New("engine: a run needs either a session count or a duration")
	case c.Auth != AuthPAP && c.Auth != AuthCHAP:
		return fmt.Errorf("engine: unknown authentication method %q", c.Auth)
	case c.accounting() && c.InterimInterval < 0:
		return errors.New("engine: interim interval cannot be negative")
	}
	return c.prepareAttributes()
}

// prepareAttributes encodes the constant attribute values once and checks the
// templated ones, so a value that cannot be encoded is reported at startup
// rather than silently dropped from every packet.
func (c *Config) prepareAttributes() error {
	c.prepared = make([]extraAttr, 0, len(c.Attributes))
	for _, spec := range c.Attributes {
		e := extraAttr{spec: spec}
		if !strings.Contains(spec.Value, "{n}") {
			v, err := spec.EncodeValue(spec.Value)
			if err != nil {
				return err
			}
			e.static = v
		} else if _, err := spec.EncodeValue(expandUsername(spec.Value, 1)); err != nil {
			return err
		}
		c.prepared = append(c.prepared, e)
	}
	return nil
}

// Metrics is the outcome of one kind of exchange.
//
// The failure counts are deliberately separate rather than rolled into one
// total. A timeout, a reply that would not verify, and a policy rejection have
// nothing to do with each other: the first suggests the server is saturated or
// unreachable, the second almost always means the shared secret is wrong, and
// the third means the server is healthy and said no. Collapsing them produces
// a number nobody can act on.
type Metrics struct {
	Name string

	Sent       uint64
	Accepted   uint64
	Rejected   uint64
	Challenged uint64
	Timeouts   uint64
	Unverified uint64
	Malformed  uint64
	Unmatched  uint64

	// Blocked counts requests that could not be offered because every
	// identifier on the chosen link was already outstanding. A non-zero value
	// means the run was limited by its own concurrency rather than by the
	// server, and the result understates what the server could take.
	Blocked uint64

	// Latency is measured from the moment a request was due to be sent
	// rather than from when it actually went out. Under overload those
	// diverge, and measuring from the actual send time quietly hides the
	// queueing delay that a stress test exists to find.
	Latency *stats.Histogram

	// ServiceTime is measured from the actual send. Comparing it against
	// Latency shows how much of the delay was the run falling behind its own
	// schedule rather than the server being slow.
	ServiceTime *stats.Histogram
}

func newMetrics(name string) *Metrics {
	return &Metrics{
		Name:        name,
		Latency:     stats.NewHistogram(),
		ServiceTime: stats.NewHistogram(),
	}
}

func (m *Metrics) merge(o *Metrics) {
	m.Sent += o.Sent
	m.Accepted += o.Accepted
	m.Rejected += o.Rejected
	m.Challenged += o.Challenged
	m.Timeouts += o.Timeouts
	m.Unverified += o.Unverified
	m.Malformed += o.Malformed
	m.Unmatched += o.Unmatched
	m.Blocked += o.Blocked
	m.Latency.Merge(o.Latency)
	m.ServiceTime.Merge(o.ServiceTime)
}

// Answered returns the number of requests that got a verified reply.
func (m *Metrics) Answered() uint64 {
	return m.Accepted + m.Rejected + m.Challenged
}

// Result is the outcome of a whole run.
type Result struct {
	// Exchanges holds one entry per kind of request, indexed by Exchange.
	Exchanges [exchangeCount]*Metrics

	Elapsed time.Duration

	// Links is how many source ports were used per endpoint. With 256
	// identifiers each, this sets the hard ceiling on requests in flight and
	// therefore on throughput for a given server response time.
	Links int

	// SessionsStarted counts sessions that reached the online state, and
	// SessionsCompleted those that were also torn down cleanly.
	SessionsStarted   uint64
	SessionsCompleted uint64

	// OfferedRate is the session setup rate the run was asked to sustain,
	// and AchievedRate what it managed over SetupWindow, the period during
	// which sessions were actually being established.
	OfferedRate  float64
	AchievedRate float64
	SetupWindow  time.Duration

	// DynAuth counts the server-initiated requests this run answered.
	DynAuth DynAuth
}

// Access returns the authentication metrics, which every run produces.
func (r *Result) Access() *Metrics { return r.Exchanges[ExchangeAccess] }

// Accounting reports whether any accounting exchange was attempted.
func (r *Result) Accounting() bool {
	for e := ExchangeAcctStart; e < exchangeCount; e++ {
		if r.Exchanges[e].Sent > 0 {
			return true
		}
	}
	return false
}

// Run drives the configured load and returns once the run is complete or ctx
// is cancelled.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	seed := cfg.Seed
	if seed == 0 {
		seed = rand.Uint64()
	}
	runID := uint32(seed)

	reg := newRegistry()

	// The dynamic authorisation listener makes this behave like an access node
	// the server can push changes to, which is the only way to test whether a
	// bulk policy change or a mass disconnect actually lands.
	var da *dynauth
	if cfg.CoAListen != "" {
		var err error
		if da, err = newDynAuth(cfg.CoAListen, cfg.Secret, reg); err != nil {
			return nil, err
		}
		defer da.close()
	}

	n := cfg.links()
	shards := make([]*shard, 0, n)
	for i := 0; i < n; i++ {
		// Each shard derives its own stream from the run seed, so the run is
		// reproducible without the shards having to coordinate.
		r := rand.New(rand.NewPCG(seed, uint64(i)))
		s, err := newShard(&cfg, i, n, runID, r, reg)
		if err != nil {
			for _, done := range shards {
				done.close()
			}
			return nil, err
		}
		shards = append(shards, s)
	}
	defer func() {
		for _, s := range shards {
			s.close()
		}
	}()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if cfg.Duration > 0 {
		var stop context.CancelFunc
		runCtx, stop = context.WithTimeout(runCtx, cfg.Duration)
		defer stop()
	}

	// Session numbers are handed out globally so every session gets a
	// distinct subscriber, whichever link carries it.
	seq := &counter{limit: cfg.Count}

	started := time.Now()

	var daWG sync.WaitGroup
	if da != nil {
		da.shards = shards
		daWG.Add(1)
		go func() {
			defer daWG.Done()
			da.serve(runCtx)
		}()
	}

	var wg sync.WaitGroup
	for _, s := range shards {
		wg.Add(1)
		go func(s *shard) {
			defer wg.Done()
			s.run(runCtx, started, seq)
		}(s)
	}
	wg.Wait()

	if da != nil {
		// Close first so the listener's pending read returns immediately
		// rather than waiting out its deadline.
		da.close()
		daWG.Wait()
	}

	elapsed := time.Since(started)

	res := &Result{
		Elapsed:     elapsed,
		Links:       n,
		OfferedRate: cfg.Rate,
	}
	for e := range res.Exchanges {
		res.Exchanges[e] = newMetrics(Exchange(e).String())
	}
	var setupEnd time.Time
	for _, s := range shards {
		s.mu.Lock()
		for e := range res.Exchanges {
			res.Exchanges[e].merge(s.metrics[e])
		}
		res.SessionsStarted += s.sessionsStarted
		res.SessionsCompleted += s.sessionsCompleted
		if s.lastSetupAt.After(setupEnd) {
			setupEnd = s.lastSetupAt
		}
		s.mu.Unlock()
	}

	// Measure the setup rate over the window sessions were actually being
	// established in. Dividing by the whole run would understate it whenever
	// sessions are held online afterwards, because most of that time is spent
	// deliberately not setting anything up.
	if da != nil {
		res.DynAuth = da.snapshot()
	}

	res.SetupWindow = setupEnd.Sub(started)
	if res.SetupWindow > 0 {
		res.AchievedRate = float64(res.Access().Sent) / res.SetupWindow.Seconds()
	} else if elapsed > 0 {
		res.AchievedRate = float64(res.Access().Sent) / elapsed.Seconds()
	}
	return res, nil
}

// counter hands out session sequence numbers across shards.
type counter struct {
	mu    sync.Mutex
	next  uint64
	limit uint64
}

// take reserves the next sequence number. It reports false once the limit is
// reached.
func (c *counter) take() (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit > 0 && c.next >= c.limit {
		return 0, false
	}
	c.next++
	return c.next, true
}

// remaining reports whether any sequence numbers are left.
func (c *counter) remaining() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limit == 0 || c.next < c.limit
}
