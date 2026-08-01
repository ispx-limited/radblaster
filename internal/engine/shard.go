package engine

import (
	"container/heap"
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// sweepInterval bounds how often a shard looks for expired requests. Requests
// outstanding on a link are capped at 256 by the identifier space, so a full
// sweep is cheap and a timer wheel would be machinery without a purpose.
const sweepInterval = 20 * time.Millisecond

// coaQueueDepth bounds the changes waiting for one shard. A full queue is
// answered with Resources-Unavailable, which is a truthful answer the server
// can act on, rather than an acknowledgement for work that was dropped.
const coaQueueDepth = 1024

// shard owns a slice of the simulated subscribers, together with the sockets
// their traffic goes out on.
//
// A session stays with one shard for its whole life, so its state is only ever
// touched by that shard and no cross-shard coordination is needed.
type shard struct {
	cfg   *Config
	index int
	total int
	runID uint32
	rng   *rand.Rand

	auth *link
	acct *link

	// stopped tells the receivers to finish. The run context is not enough on
	// its own: a run bounded by session count completes without the context
	// ever being cancelled, and a receiver would otherwise keep looping on
	// read timeouts forever.
	stopped atomic.Bool

	// reg is the run-wide index a server-initiated request is resolved
	// through, and coa carries the resulting changes to this shard so a
	// session is only ever mutated by the worker that owns it.
	reg *registry
	coa chan coaEvent

	// mu guards everything below. The critical sections are short and only
	// three goroutines contend for them, so a mutex is cheaper here than the
	// bookkeeping needed to avoid one.
	mu       sync.Mutex
	sessions map[uint32]*session
	due      sessionQueue
	nextIdx  uint32

	metrics           [exchangeCount]*Metrics
	sessionsStarted   uint64
	sessionsCompleted uint64

	// lastSetupAt is when this shard last authenticated a new session. The
	// achieved setup rate is measured over the setup window rather than the
	// whole run, because a run that holds sessions online afterwards spends
	// most of its time deliberately not setting anything up.
	lastSetupAt time.Time
}

func newShard(cfg *Config, index, total int, runID uint32, r *rand.Rand, reg *registry) (*shard, error) {
	s := &shard{
		cfg:      cfg,
		index:    index,
		total:    total,
		runID:    runID,
		rng:      r,
		sessions: make(map[uint32]*session),
		reg:      reg,
		coa:      make(chan coaEvent, coaQueueDepth),
	}
	for e := range s.metrics {
		s.metrics[e] = newMetrics(Exchange(e).String())
	}

	var err error
	if s.auth, err = newLink(cfg.Server, cfg.Secret, r); err != nil {
		return nil, err
	}
	if cfg.accounting() {
		if s.acct, err = newLink(cfg.AcctServer, cfg.Secret, r); err != nil {
			s.auth.close()
			return nil, err
		}
	}
	return s, nil
}

func (s *shard) close() {
	s.auth.close()
	s.acct.close()
}

// run sets sessions up until ctx ends or the counter is exhausted, then keeps
// driving the ones already online until they finish.
func (s *shard) run(ctx context.Context, started time.Time, seq *counter) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.receive(ctx, s.auth)
	}()
	if s.acct != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.receive(ctx, s.acct)
		}()
	}

	s.loop(ctx, started, seq)

	// Requests already on the wire deserve their full timeout to be answered.
	// Stopping the moment the last one is sent would count the replies still
	// in flight as losses this run caused itself.
	s.drain(ctx)

	s.stopped.Store(true)
	_ = s.auth.conn.SetReadDeadline(time.Now())
	if s.acct != nil {
		_ = s.acct.conn.SetReadDeadline(time.Now())
	}
	wg.Wait()

	s.expireAll()
}

// schedule returns the gap between this shard's own session setups and the
// offset that staggers it against the other shards.
func (s *shard) schedule() (interval, phase time.Duration) {
	if s.cfg.Rate <= 0 {
		return 0, 0
	}
	interval = time.Duration(float64(time.Second) * float64(s.total) / s.cfg.Rate)
	phase = time.Duration(float64(time.Second) * float64(s.index) / s.cfg.Rate)
	return interval, phase
}

func (s *shard) loop(ctx context.Context, started time.Time, seq *counter) {
	interval, phase := s.schedule()
	unpaced := interval == 0

	var setups uint64
	nextSetup := started.Add(phase)
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

		// Apply anything the server asked for before advancing sessions, so
		// a disconnect takes effect rather than racing the next update.
		s.drainCoA(now)

		// Drive every session whose next step has come due.
		s.advanceDue(now)

		// Offer new sessions whose setup moment has arrived.
		const maxBatch = 256
		batch := 0
		blocked := false
		setupsDone := false
		for unpaced || !nextSetup.After(now) {
			if !s.canSetup() {
				s.countBlocked(ExchangeAccess)
				blocked = true
				break
			}
			n, ok := seq.take()
			if !ok {
				setupsDone = true
				break
			}
			var dueAt time.Time
			if !unpaced {
				dueAt = nextSetup
			}
			s.startSession(n, dueAt)
			setups++
			batch++

			if !unpaced {
				nextSetup = started.Add(phase + time.Duration(setups)*interval)
			}
			if batch >= maxBatch {
				break
			}
			if ctx.Err() != nil {
				return
			}
		}

		// The run is over once no more sessions can be set up and every
		// session already online has finished its lifecycle.
		if (setupsDone || !seq.remaining()) && s.idle() {
			return
		}

		s.wait(ctx, now, nextSetup, unpaced, blocked, setupsDone)
	}
}

// wait sleeps until the next thing needs doing, bounded so the loop keeps
// checking the context and sweeping for timeouts.
func (s *shard) wait(ctx context.Context, now, nextSetup time.Time, unpaced, blocked, setupsDone bool) {
	sleep := sweepInterval

	switch {
	case blocked:
		// Every identifier is outstanding, so pushing harder would only spin
		// a core recording refusals.
		sleep = time.Millisecond
	case unpaced && !setupsDone:
		return
	default:
		if next, ok := s.nextDue(); ok {
			if d := next.Sub(now); d < sleep {
				sleep = d
			}
		}
		if !setupsDone {
			if d := nextSetup.Sub(now); d >= 0 && d < sleep {
				sleep = d
			}
		}
	}
	if sleep <= 0 {
		return
	}

	timer := time.NewTimer(sleep)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// drain waits for outstanding requests to be answered or to time out.
func (s *shard) drain(ctx context.Context) {
	deadline := time.Now().Add(s.cfg.timeout())
	for time.Now().Before(deadline) {
		if s.pendingCount() == 0 {
			return
		}
		s.expire(time.Now())

		timer := time.NewTimer(sweepInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// pendingCount reports how many requests are awaiting a reply.
func (s *shard) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range []*link{s.auth, s.acct} {
		if l != nil {
			n += radius.IDSpace - l.ids.Available()
		}
	}
	return n
}

// submitCoA hands a server-initiated change to this shard. It reports false
// when the queue is full, which the caller answers with Resources-Unavailable
// rather than dropping the request silently.
func (s *shard) submitCoA(ev coaEvent) bool {
	select {
	case s.coa <- ev:
		return true
	default:
		return false
	}
}

// drainCoA applies every change waiting for this shard.
func (s *shard) drainCoA(now time.Time) {
	for {
		select {
		case ev := <-s.coa:
			s.applyCoA(ev, now)
		default:
			return
		}
	}
}

// applyCoA carries out a change the server asked for.
func (s *shard) applyCoA(ev coaEvent, now time.Time) {
	s.mu.Lock()
	sess := s.sessions[ev.index]
	if sess == nil || sess.state != stateActive {
		// The session finished between the listener acknowledging the request
		// and this shard getting to it.
		s.mu.Unlock()
		return
	}
	// Take it out of the queue while its schedule is rewritten, so it cannot
	// be advanced from under us.
	if sess.queueIdx >= 0 {
		heap.Remove(&s.due, sess.queueIdx)
	}
	s.mu.Unlock()

	if ev.disconnect {
		// A subscriber removed by the server still owes a final accounting
		// record, and the cause has to say who ended it.
		sess.state = stateAcctStop
		s.sendAcct(sess, ExchangeAcctStop, acctStop, now, now, causeAdminReset)
		s.finish(sess, true)
		return
	}

	if ev.interim > 0 {
		sess.interim = ev.interim
	}
	if ev.sessionTimeout > 0 {
		sess.stopAt = sess.onlineAt.Add(ev.sessionTimeout)
	}
	s.scheduleNext(sess, now)
}

// canSetup reports whether a new session can be authenticated right now.
func (s *shard) canSetup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth.available()
}

// idle reports whether the shard has no sessions left to drive.
func (s *shard) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions) == 0
}

// nextDue returns when the earliest scheduled session step falls.
func (s *shard) nextDue() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.due.Len() == 0 {
		return time.Time{}, false
	}
	return s.due[0].nextAt, true
}

func (s *shard) countBlocked(e Exchange) {
	s.mu.Lock()
	s.metrics[e].Blocked++
	s.mu.Unlock()
}

// startSession creates a subscriber and authenticates it.
func (s *shard) startSession(seq uint64, dueAt time.Time) {
	s.mu.Lock()
	idx := s.nextIdx
	s.nextIdx++
	sess := newSession(idx, seq, s.cfg, s.runID)
	s.sessions[idx] = sess
	s.lastSetupAt = time.Now()
	s.mu.Unlock()

	s.sendAccess(sess, dueAt)
}

// advanceDue drives every session whose scheduled step has arrived.
func (s *shard) advanceDue(now time.Time) {
	for {
		s.mu.Lock()
		if s.due.Len() == 0 || s.due[0].nextAt.After(now) {
			s.mu.Unlock()
			return
		}
		sess := heap.Pop(&s.due).(*session)
		s.mu.Unlock()

		s.step(sess, now)
	}
}

// step performs the session's next lifecycle action.
func (s *shard) step(sess *session, now time.Time) {
	switch sess.state {
	case stateAcctStart:
		sess.onlineAt = now
		sess.stopAt = now.Add(s.cfg.Hold)
		sess.state = stateActive
		s.sendAcct(sess, ExchangeAcctStart, acctStart, sess.nextAt, now, 0)

		s.mu.Lock()
		s.sessionsStarted++
		s.mu.Unlock()

		s.scheduleNext(sess, now)

	case stateActive:
		if !now.Before(sess.stopAt) {
			sess.state = stateAcctStop
			s.sendAcct(sess, ExchangeAcctStop, acctStop, sess.nextAt, now, 0)
			s.finish(sess, true)
			return
		}
		s.sendAcct(sess, ExchangeAcctInterim, acctInterim, sess.nextAt, now, 0)
		s.scheduleNext(sess, now)

	default:
		s.finish(sess, false)
	}
}

// scheduleNext queues the session's following step, which is either its next
// interim update or its teardown, whichever comes first.
func (s *shard) scheduleNext(sess *session, now time.Time) {
	next := sess.stopAt
	if sess.interim > 0 {
		if t := now.Add(sess.interim); t.Before(next) {
			next = t
		}
	}
	if next.Before(now) {
		next = now
	}
	sess.nextAt = next

	s.mu.Lock()
	heap.Push(&s.due, sess)
	s.mu.Unlock()
}

// finish removes a session from the shard.
func (s *shard) finish(sess *session, completed bool) {
	sess.state = stateDone
	if s.reg != nil {
		s.reg.remove(sess.acctSessionID)
	}
	s.mu.Lock()
	delete(s.sessions, sess.index)
	if completed {
		s.sessionsCompleted++
	}
	s.mu.Unlock()
}

// sendAccess builds and sends the session's Access-Request.
func (s *shard) sendAccess(sess *session, dueAt time.Time) {
	s.mu.Lock()
	id, ok := s.auth.ids.Get()
	if !ok {
		s.metrics[ExchangeAccess].Blocked++
		delete(s.sessions, sess.index)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	p, err := radius.New(radius.CodeAccessRequest, id)
	if err != nil {
		s.abort(s.auth, id, sess, ExchangeAccess)
		return
	}
	if err := sess.buildAccess(p, s.cfg, s.rng); err != nil {
		s.abort(s.auth, id, sess, ExchangeAccess)
		return
	}

	sess.state = stateAuthenticating
	s.transmit(s.auth, p, sess, ExchangeAccess, id, dueAt)
}

// sendAcct builds and sends one accounting record.
func (s *shard) sendAcct(sess *session, e Exchange, status uint32, dueAt, now time.Time, cause uint32) {
	if s.acct == nil {
		return
	}

	s.mu.Lock()
	id, ok := s.acct.ids.Get()
	if !ok {
		s.metrics[e].Blocked++
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	p, err := radius.New(radius.CodeAccountingRequest, id)
	if err != nil {
		s.mu.Lock()
		s.acct.ids.Put(id)
		s.metrics[e].Malformed++
		s.mu.Unlock()
		return
	}
	sess.buildAcct(p, s.cfg, status, now, cause)

	s.transmit(s.acct, p, sess, e, id, dueAt)
}

// abort releases an identifier for a request that could not be built.
func (s *shard) abort(l *link, id uint8, sess *session, e Exchange) {
	s.mu.Lock()
	l.ids.Put(id)
	s.metrics[e].Malformed++
	delete(s.sessions, sess.index)
	s.mu.Unlock()
}

// transmit encodes, records and sends one request.
func (s *shard) transmit(l *link, p *radius.Packet, sess *session, e Exchange, id uint8, dueAt time.Time) {
	s.mu.Lock()
	wire, err := l.enc.Encode(p)
	if err != nil {
		l.ids.Put(id)
		s.metrics[e].Malformed++
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
	l.pending[id] = pending{
		active:   true,
		session:  sess.index,
		exchange: e,
		auth:     p.Authenticator,
		dueAt:    dueAt,
		sentAt:   sentAt,
	}
	s.metrics[e].Sent++

	// The encoder's buffer is reused on its next call, so the bytes must go
	// out before the lock is released.
	_, werr := l.conn.Write(wire)
	if werr != nil {
		l.pending[id].active = false
		l.ids.Put(id)
		s.metrics[e].Sent--
		s.metrics[e].Malformed++
	}
	s.mu.Unlock()
}

func (s *shard) receive(ctx context.Context, l *link) {
	buf := make([]byte, radius.MaxPacketLen)
	for {
		if s.stopped.Load() || ctx.Err() != nil {
			return
		}
		// A deadline keeps the read from parking forever once the run is
		// over, without needing to close the socket from another goroutine.
		_ = l.conn.SetReadDeadline(time.Now().Add(sweepInterval))

		n, err := l.conn.Read(buf)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			return
		}
		s.handle(l, buf[:n], time.Now())
	}
}

// handle matches a reply to its request and records the outcome.
func (s *shard) handle(l *link, reply []byte, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(reply) < radius.HeaderLen {
		s.metrics[ExchangeAccess].Malformed++
		return
	}
	id := reply[1]
	p := &l.pending[id]
	if !p.active {
		// A reply to something already given up on, or to nothing at all.
		s.metrics[ExchangeAccess].Unmatched++
		return
	}
	m := s.metrics[p.exchange]

	if err := radius.Verify(reply, p.auth, s.cfg.Secret); err != nil {
		// Do not release the identifier here. A forged or mis-signed packet
		// must not be able to cancel a request that is still legitimately
		// outstanding, or an attacker could free identifiers at will.
		m.Unverified++
		return
	}

	decoded, err := radius.Decode(reply)
	if err != nil {
		m.Malformed++
		return
	}

	sess := s.sessions[p.session]

	switch decoded.Code {
	case radius.CodeAccessAccept:
		m.Accepted++
		if sess != nil {
			sess.applyAccept(decoded, s.cfg)
			s.onAuthenticated(sess, at)
		}
	case radius.CodeAccessReject:
		m.Rejected++
		if sess != nil {
			delete(s.sessions, sess.index)
			sess.state = stateDone
		}
	case radius.CodeAccessChallenge:
		m.Challenged++
		if sess != nil {
			delete(s.sessions, sess.index)
			sess.state = stateDone
		}
	case radius.CodeAccountingResponse:
		m.Accepted++
	default:
		m.Malformed++
	}

	m.Latency.Record(uint64(at.Sub(p.dueAt)))
	m.ServiceTime.Record(uint64(at.Sub(p.sentAt)))

	p.active = false
	l.ids.Put(id)
}

// onAuthenticated moves a session from authenticating to online. The caller
// holds the lock.
func (s *shard) onAuthenticated(sess *session, at time.Time) {
	if !s.cfg.accounting() {
		// Without accounting a session is only an authentication, so it is
		// finished as soon as it succeeds.
		delete(s.sessions, sess.index)
		sess.state = stateDone
		s.sessionsStarted++
		s.sessionsCompleted++
		return
	}
	sess.state = stateAcctStart
	sess.nextAt = at
	if s.reg != nil {
		s.reg.add(sess.acctSessionID, sessionRef{shard: s.index, index: sess.index})
	}
	heap.Push(&s.due, sess)
}

// expire releases requests that have gone unanswered past the timeout.
func (s *shard) expire(now time.Time) {
	timeout := s.cfg.timeout()

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range []*link{s.auth, s.acct} {
		if l == nil {
			continue
		}
		for i := range l.pending {
			p := &l.pending[i]
			if !p.active || now.Sub(p.sentAt) < timeout {
				continue
			}
			p.active = false
			s.metrics[p.exchange].Timeouts++
			l.ids.Put(uint8(i))

			// A session whose authentication was never answered cannot go
			// online, so it is finished rather than left in the table.
			if p.exchange == ExchangeAccess {
				if sess := s.sessions[p.session]; sess != nil && sess.state == stateAuthenticating {
					delete(s.sessions, sess.index)
					sess.state = stateDone
				}
			}
		}
	}
}

// expireAll counts anything still outstanding once the run has stopped.
func (s *shard) expireAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range []*link{s.auth, s.acct} {
		if l == nil {
			continue
		}
		for i := range l.pending {
			if l.pending[i].active {
				l.pending[i].active = false
				s.metrics[l.pending[i].exchange].Timeouts++
			}
		}
	}
}

// sessionQueue orders sessions by when their next lifecycle step falls due.
//
// Each session tracks its own position so a server-initiated disconnect can
// pull it out of the queue directly rather than scanning for it.
type sessionQueue []*session

func (q sessionQueue) Len() int           { return len(q) }
func (q sessionQueue) Less(i, j int) bool { return q[i].nextAt.Before(q[j].nextAt) }

func (q sessionQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].queueIdx = i
	q[j].queueIdx = j
}

func (q *sessionQueue) Push(x any) {
	s := x.(*session)
	s.queueIdx = len(*q)
	*q = append(*q, s)
}

func (q *sessionQueue) Pop() any {
	old := *q
	n := len(old)
	s := old[n-1]
	old[n-1] = nil
	s.queueIdx = -1
	*q = old[:n-1]
	return s
}
