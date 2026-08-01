package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// sweepInterval bounds how often a shard looks for expired requests. Pending
// requests are capped at 256 by the identifier space, so a full sweep is
// cheap and a timer wheel would be machinery without a purpose.
const sweepInterval = 20 * time.Millisecond

// pending is one outstanding request.
type pending struct {
	active bool
	seq    uint64
	auth   [radius.AuthenticatorLen]byte
	// dueAt is when the request was scheduled to go out, deadline for the
	// latency measurement. sentAt is when it actually did.
	dueAt  time.Time
	sentAt time.Time
}

// shard owns one source port, its identifier space, and the requests
// outstanding on it.
//
// Concurrency comes from sockets rather than goroutines, because the RADIUS
// identifier is only eight bits wide: one socket can carry at most 256
// requests at a time regardless of how many goroutines push at it.
type shard struct {
	cfg   *Config
	index int
	total int

	conn *net.UDPConn
	enc  *radius.Encoder
	rng  *rand.Rand

	// stopped tells the receiver to finish. The run context is not enough on
	// its own: a run bounded by request count completes without the context
	// ever being cancelled, and the receiver would otherwise keep looping on
	// read timeouts forever.
	stopped atomic.Bool

	// mu guards everything below. The critical sections are a few hundred
	// nanoseconds and only two goroutines contend for them, so a mutex is
	// cheaper here than the bookkeeping needed to avoid one.
	mu      sync.Mutex
	ids     *radius.IDPool
	pending [radius.IDSpace]pending
	metrics *Metrics
}

func newShard(cfg *Config, index, total int, r *rand.Rand) (*shard, error) {
	addr, err := net.ResolveUDPAddr("udp", cfg.Server)
	if err != nil {
		return nil, fmt.Errorf("engine: resolving %s: %w", cfg.Server, err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("engine: opening socket to %s: %w", cfg.Server, err)
	}
	return &shard{
		cfg:     cfg,
		index:   index,
		total:   total,
		conn:    conn,
		enc:     radius.NewEncoder(cfg.Secret),
		rng:     r,
		ids:     radius.NewIDPool(r),
		metrics: newMetrics("Access-Request"),
	}, nil
}

func (s *shard) close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// run offers load until ctx ends or the sequence counter is exhausted, then
// waits out the timeout so replies still in flight are counted.
func (s *shard) run(ctx context.Context, started time.Time, seq *counter) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.receive(ctx)
	}()

	s.send(ctx, started, seq)

	// Give outstanding requests their full timeout to be answered rather
	// than recording them as losses we caused by stopping.
	s.drain(ctx)

	s.stopped.Store(true)
	_ = s.conn.SetReadDeadline(time.Now())
	<-done

	s.expireAll()
}

// interval is the gap between this shard's own sends, and phase staggers the
// shards so they interleave instead of firing together.
func (s *shard) schedule() (interval, phase time.Duration) {
	if s.cfg.Rate <= 0 {
		return 0, 0
	}
	perSecond := s.cfg.Rate
	interval = time.Duration(float64(time.Second) * float64(s.total) / perSecond)
	phase = time.Duration(float64(time.Second) * float64(s.index) / perSecond)
	return interval, phase
}

func (s *shard) send(ctx context.Context, started time.Time, seq *counter) {
	interval, phase := s.schedule()
	unpaced := interval == 0

	var sent uint64
	due := started.Add(phase)
	lastSweep := started

	for {
		if ctx.Err() != nil {
			return
		}

		now := time.Now()
		if now.Sub(lastSweep) >= sweepInterval {
			s.expire(now)
			lastSweep = now
		}

		// Send everything whose scheduled moment has arrived. The batch is
		// bounded so a run that has fallen behind still returns to check the
		// clock and the context rather than spinning.
		const maxBatch = 256
		batch := 0
		blocked := false
		for unpaced || !due.After(now) {
			// Check for a free identifier before claiming a sequence number,
			// so a request that cannot be offered does not consume part of a
			// bounded run's budget.
			if !s.hasIdentifier() {
				s.countBlocked()
				blocked = true
				break
			}
			n, ok := seq.take()
			if !ok {
				return
			}
			// Unpaced runs have no schedule to be late against, so the
			// request is due when it is sent. Passing the zero time asks
			// offer to use its own send timestamp.
			var dueAt time.Time
			if !unpaced {
				dueAt = due
			}
			s.offer(n, dueAt)
			sent++
			batch++

			if !unpaced {
				due = started.Add(phase + time.Duration(sent)*interval)
			}
			if batch >= maxBatch {
				break
			}
			if ctx.Err() != nil {
				return
			}
		}

		if blocked {
			// Every identifier on this socket is outstanding, so offering
			// more would only spin a core recording refusals. Wait for the
			// receiver or the sweep to free one.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
			continue
		}

		if unpaced {
			continue
		}

		wait := time.Until(due)
		if wait <= 0 {
			continue
		}
		if wait > sweepInterval {
			wait = sweepInterval
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// hasIdentifier reports whether this socket can carry another request.
func (s *shard) hasIdentifier() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ids.Available() > 0
}

// countBlocked records that load could not be offered because every
// identifier on this socket was outstanding. Keeping this visible matters: a
// non-zero count means the run was limited by our own concurrency rather than
// by the server, so the result understates what the server could take.
func (s *shard) countBlocked() {
	s.mu.Lock()
	s.metrics.Blocked++
	s.mu.Unlock()
}

// offer builds and sends one Access-Request.
//
// dueAt is when the request was scheduled to go out, which is what latency is
// measured against. A zero dueAt means there was no schedule, so the send time
// is used instead.
func (s *shard) offer(seq uint64, dueAt time.Time) {
	s.mu.Lock()
	id, ok := s.ids.Get()
	if !ok {
		s.metrics.Blocked++
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	p, err := radius.New(radius.CodeAccessRequest, id)
	if err != nil {
		s.mu.Lock()
		s.ids.Put(id)
		s.metrics.Malformed++
		s.mu.Unlock()
		return
	}

	username := expandUsername(s.cfg.Username, seq)
	p.AddString(radius.AttrUserName, username)

	switch s.cfg.Auth {
	case AuthCHAP:
		ident := uint8(s.rng.UintN(256))
		// With no CHAP-Challenge attribute the request authenticator serves
		// as the challenge, per RFC 2865 section 5.3.
		resp := radius.CHAPPassword(ident, []byte(s.cfg.Password), p.Authenticator[:])
		p.Add(radius.AttrCHAPPassword, resp[:])
	default:
		enc, err := radius.EncryptUserPassword(nil, []byte(s.cfg.Password), s.cfg.Secret, p.Authenticator)
		if err != nil {
			s.mu.Lock()
			s.ids.Put(id)
			s.metrics.Malformed++
			s.mu.Unlock()
			return
		}
		p.Add(radius.AttrUserPassword, enc)
	}

	if s.cfg.NASIdentifier != "" {
		p.AddString(radius.AttrNASIdentifier, s.cfg.NASIdentifier)
	}
	p.AddUint32(radius.AttrServiceType, 2)    // Framed-User
	p.AddUint32(radius.AttrFramedProtocol, 1) // PPP

	wire, err := s.enc.Encode(p)
	if err != nil {
		s.mu.Lock()
		s.ids.Put(id)
		s.metrics.Malformed++
		s.mu.Unlock()
		return
	}

	// Timestamp per packet rather than per batch. A batch can carry hundreds
	// of requests, and reusing one timestamp across them would overstate how
	// long the server took for every packet but the first.
	sentAt := time.Now()
	if dueAt.IsZero() {
		dueAt = sentAt
	}

	s.mu.Lock()
	s.pending[id] = pending{
		active: true,
		seq:    seq,
		auth:   p.Authenticator,
		dueAt:  dueAt,
		sentAt: sentAt,
	}
	s.metrics.Sent++
	s.mu.Unlock()

	if _, err := s.conn.Write(wire); err != nil {
		s.mu.Lock()
		if s.pending[id].active && s.pending[id].seq == seq {
			s.pending[id].active = false
			s.ids.Put(id)
			s.metrics.Sent--
			s.metrics.Malformed++
		}
		s.mu.Unlock()
	}
}

func (s *shard) receive(ctx context.Context) {
	buf := make([]byte, radius.MaxPacketLen)
	for {
		if s.stopped.Load() || ctx.Err() != nil {
			return
		}
		// A deadline keeps the read from parking forever once the run is
		// over, without needing to close the socket from another goroutine.
		_ = s.conn.SetReadDeadline(time.Now().Add(sweepInterval))

		n, err := s.conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		s.handle(buf[:n], time.Now())
	}
}

// handle matches a reply to its request and records the outcome.
func (s *shard) handle(reply []byte, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(reply) < radius.HeaderLen {
		s.metrics.Malformed++
		return
	}
	id := reply[1]
	p := &s.pending[id]
	if !p.active {
		// A reply to something we already gave up on, or to nothing at all.
		s.metrics.Unmatched++
		return
	}

	if err := radius.Verify(reply, p.auth, s.cfg.Secret); err != nil {
		// Do not release the identifier here. A forged or mis-signed packet
		// must not be able to cancel a request that is still legitimately
		// outstanding, or an attacker could free identifiers at will.
		s.metrics.Unverified++
		return
	}

	decoded, err := radius.Decode(reply)
	if err != nil {
		s.metrics.Malformed++
		return
	}

	switch decoded.Code {
	case radius.CodeAccessAccept:
		s.metrics.Accepted++
	case radius.CodeAccessReject:
		s.metrics.Rejected++
	case radius.CodeAccessChallenge:
		s.metrics.Challenged++
	default:
		s.metrics.Malformed++
	}

	s.metrics.Latency.Record(uint64(at.Sub(p.dueAt)))
	s.metrics.ServiceTime.Record(uint64(at.Sub(p.sentAt)))

	p.active = false
	s.ids.Put(id)
}

// expire releases requests that have gone unanswered past the timeout.
func (s *shard) expire(now time.Time) {
	timeout := s.cfg.timeout()

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pending {
		p := &s.pending[i]
		if !p.active || now.Sub(p.sentAt) < timeout {
			continue
		}
		p.active = false
		s.metrics.Timeouts++
		s.ids.Put(uint8(i))
	}
}

// drain waits for outstanding requests to be answered or to time out.
func (s *shard) drain(ctx context.Context) {
	deadline := time.Now().Add(s.cfg.timeout())
	for time.Now().Before(deadline) {
		s.mu.Lock()
		outstanding := radius.IDSpace - s.ids.Available()
		s.mu.Unlock()
		if outstanding == 0 {
			return
		}

		s.expire(time.Now())

		select {
		case <-ctx.Done():
			// Cancellation stops the run, but the requests already sent are
			// still accounted for by expireAll.
			return
		case <-time.After(sweepInterval):
		}
	}
}

// expireAll counts anything still outstanding once the run has stopped.
func (s *shard) expireAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pending {
		if s.pending[i].active {
			s.pending[i].active = false
			s.metrics.Timeouts++
		}
	}
}

// expandUsername substitutes the request sequence number into a template.
func expandUsername(tmpl string, seq uint64) string {
	if !strings.Contains(tmpl, "{n}") {
		return tmpl
	}
	return strings.ReplaceAll(tmpl, "{n}", strconv.FormatUint(seq, 10))
}
