// Package engine drives RADIUS load against a server and measures the result.
package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"time"

	"github.com/ispx-limited/radblaster/internal/stats"
)

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
	// Server is the target, as host:port.
	Server string
	// Secret is the shared secret.
	Secret []byte
	// Rate is the offered requests per second across the whole run. Zero
	// means send as fast as the sockets allow.
	Rate float64
	// Count bounds the run by number of requests. Zero means unbounded.
	Count uint64
	// Duration bounds the run by wall-clock time. Zero means unbounded.
	Duration time.Duration
	// Sockets is the number of source ports to spread across. Each carries
	// its own identifier space, so this sets the ceiling on outstanding
	// requests at 256 per socket. Zero picks a sensible default.
	Sockets int
	// Timeout is how long a request may go unanswered before it is counted
	// as lost.
	Timeout time.Duration
	// Auth selects PAP or CHAP.
	Auth Auth
	// Username is a template; {n} is replaced with the request sequence
	// number, starting at 1.
	Username string
	// Password is the cleartext to authenticate with.
	Password string
	// NASIdentifier, when set, is sent as NAS-Identifier.
	NASIdentifier string
	// Seed makes a run reproducible. Zero draws a fresh seed.
	Seed uint64
}

func (c *Config) sockets() int {
	if c.Sockets > 0 {
		return c.Sockets
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

func (c *Config) validate() error {
	switch {
	case c.Server == "":
		return errors.New("engine: no server given")
	case len(c.Secret) == 0:
		return errors.New("engine: no shared secret given")
	case c.Username == "":
		return errors.New("engine: no username given")
	case c.Count == 0 && c.Duration == 0:
		return errors.New("engine: a run needs either a request count or a duration")
	case c.Auth != AuthPAP && c.Auth != AuthCHAP:
		return fmt.Errorf("engine: unknown authentication method %q", c.Auth)
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
	// identifier on the chosen socket was already outstanding. A non-zero
	// value means the run was limited by our own concurrency rather than by
	// the server, and the result understates what the server could take.
	Blocked uint64

	// Latency is measured from the moment a request was *due* to be sent
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
	Access  *Metrics
	Elapsed time.Duration

	// Sockets is how many source ports the run used. With 256 identifiers
	// each, this sets the hard ceiling on requests in flight, and therefore
	// on throughput for a given server response time.
	Sockets int

	// OfferedRate is the rate the run was asked to sustain, and AchievedRate
	// what it managed. A gap between them means the run could not keep to its
	// own schedule, so the latency figures should be read as the server being
	// measured under less load than intended.
	OfferedRate  float64
	AchievedRate float64
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

	n := cfg.sockets()
	shards := make([]*shard, 0, n)
	for i := 0; i < n; i++ {
		// Each shard derives its own stream from the run seed, so the run is
		// reproducible without the shards having to coordinate.
		r := rand.New(rand.NewPCG(seed, uint64(i)))
		s, err := newShard(&cfg, i, n, r)
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

	// Sequence numbers are handed out globally so every request gets a
	// distinct subscriber, whichever socket carries it.
	seq := &counter{}
	if cfg.Count > 0 {
		seq.limit = cfg.Count
	}

	started := time.Now()

	var wg sync.WaitGroup
	for _, s := range shards {
		wg.Add(1)
		go func(s *shard) {
			defer wg.Done()
			s.run(runCtx, started, seq)
		}(s)
	}
	wg.Wait()

	elapsed := time.Since(started)

	total := newMetrics("Access-Request")
	for _, s := range shards {
		s.mu.Lock()
		total.merge(s.metrics)
		s.mu.Unlock()
	}

	res := &Result{
		Access:      total,
		Elapsed:     elapsed,
		Sockets:     n,
		OfferedRate: cfg.Rate,
	}
	if elapsed > 0 {
		res.AchievedRate = float64(total.Sent) / elapsed.Seconds()
	}
	return res, nil
}

// counter hands out request sequence numbers across shards.
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
