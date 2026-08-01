package engine

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// Snapshot is a point-in-time view of a run that is still going.
//
// Counters are cumulative since the run began; the per-second figures are
// measured over the interval since the previous snapshot, so they show what is
// happening now rather than an average dragged down by everything before it.
type Snapshot struct {
	Elapsed time.Duration

	// Exchanges holds cumulative metrics, indexed by Exchange.
	Exchanges [exchangeCount]*Metrics

	SessionsStarted   uint64
	SessionsCompleted uint64
	// SessionsOnline is how many subscribers are online right now.
	SessionsOnline uint64
	// Outstanding is how many requests are awaiting a reply. It is the number
	// that shows a server falling behind before any latency figure moves.
	Outstanding int

	DynAuth DynAuth

	// SetupRate, AnswerRate and LossRate are per second over the interval
	// since the previous snapshot.
	SetupRate  float64
	AnswerRate float64
	LossRate   float64
}

// Access returns the authentication metrics.
func (s *Snapshot) Access() *Metrics { return s.Exchanges[ExchangeAccess] }

// Runner is a run in progress.
//
// Callers that only want the outcome should use Run. Start and Wait exist so a
// live view can watch a run while it happens, which a single blocking call
// cannot offer.
type Runner struct {
	cfg     Config
	shards  []*shard
	da      *dynauth
	links   int
	started time.Time

	cancel context.CancelFunc
	stop   context.CancelFunc
	daWG   sync.WaitGroup
	wg     sync.WaitGroup

	done     chan struct{}
	waitOnce sync.Once
	result   *Result

	// mu guards the previous snapshot, used to turn cumulative counters into
	// the rates over the last interval.
	mu       sync.Mutex
	prevAt   time.Duration
	prevSent uint64
	prevOK   uint64
	prevLost uint64
}

// Run drives the configured load and returns once the run is complete or ctx
// is cancelled.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	r, err := Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return r.Wait()
}

// Start begins a run and returns immediately. The caller must call Wait, which
// releases the sockets the run holds.
func Start(ctx context.Context, cfg Config) (*Runner, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	seed := cfg.Seed
	if seed == 0 {
		seed = rand.Uint64()
	}
	runID := uint32(seed)

	reg := newRegistry()

	r := &Runner{cfg: cfg, links: cfg.links(), done: make(chan struct{})}

	// The dynamic authorisation listener makes this behave like an access node
	// the server can push changes to, which is the only way to test whether a
	// bulk policy change or a mass disconnect actually lands.
	if cfg.CoAListen != "" {
		da, err := newDynAuth(cfg.CoAListen, cfg.Secret, reg)
		if err != nil {
			return nil, err
		}
		r.da = da
	}

	for i := 0; i < r.links; i++ {
		// Each shard derives its own stream from the run seed, so the run is
		// reproducible without the shards having to coordinate.
		rng := rand.New(rand.NewPCG(seed, uint64(i)))
		s, err := newShard(&r.cfg, i, r.links, runID, rng, reg)
		if err != nil {
			for _, done := range r.shards {
				done.close()
			}
			r.da.close()
			return nil, err
		}
		r.shards = append(r.shards, s)
	}

	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	if cfg.Duration > 0 {
		runCtx, r.stop = context.WithTimeout(runCtx, cfg.Duration)
	}

	// Session numbers are handed out globally so every session gets a
	// distinct subscriber, whichever link carries it.
	seq := &counter{limit: cfg.Count}
	r.started = time.Now()

	if r.da != nil {
		r.da.shards = r.shards
		r.daWG.Add(1)
		go func() {
			defer r.daWG.Done()
			r.da.serve(runCtx)
		}()
	}

	for _, s := range r.shards {
		r.wg.Add(1)
		go func(s *shard) {
			defer r.wg.Done()
			s.run(runCtx, r.started, seq)
		}(s)
	}

	go func() {
		r.wg.Wait()
		close(r.done)
	}()

	return r, nil
}

// Done returns a channel closed when the run has finished.
func (r *Runner) Done() <-chan struct{} { return r.done }

// Stop asks the run to finish early. Wait still has to be called.
func (r *Runner) Stop() { r.cancel() }

// Wait blocks until the run finishes and returns its result. It is safe to
// call more than once; later calls return the same result.
func (r *Runner) Wait() (*Result, error) {
	r.waitOnce.Do(func() {
		<-r.done

		if r.da != nil {
			// Close first so the listener's pending read returns immediately
			// rather than waiting out its deadline.
			r.da.close()
			r.daWG.Wait()
		}
		if r.stop != nil {
			r.stop()
		}
		r.cancel()

		r.result = r.collect()

		for _, s := range r.shards {
			s.close()
		}
	})
	return r.result, nil
}

// collect merges every shard's measurements into the final result.
func (r *Runner) collect() *Result {
	elapsed := time.Since(r.started)

	res := &Result{
		Elapsed:     elapsed,
		Links:       r.links,
		OfferedRate: r.cfg.Rate,
	}
	for e := range res.Exchanges {
		res.Exchanges[e] = newMetrics(Exchange(e).String())
	}

	var setupEnd time.Time
	for _, s := range r.shards {
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

	if r.da != nil {
		res.DynAuth = r.da.snapshot()
	}

	// Measure the setup rate over the window sessions were actually being
	// established in. Dividing by the whole run would understate it whenever
	// sessions are held online afterwards, because most of that time is spent
	// deliberately not setting anything up.
	res.SetupWindow = setupEnd.Sub(r.started)
	if res.SetupWindow > 0 {
		res.AchievedRate = float64(res.Access().Sent) / res.SetupWindow.Seconds()
	} else if elapsed > 0 {
		res.AchievedRate = float64(res.Access().Sent) / elapsed.Seconds()
	}
	return res
}

// Snapshot returns what the run looks like right now.
//
// It is safe to call at any time, including after the run has finished, and
// from a different goroutine than the one that started it.
func (r *Runner) Snapshot() Snapshot {
	snap := Snapshot{Elapsed: time.Since(r.started)}
	for e := range snap.Exchanges {
		snap.Exchanges[e] = newMetrics(Exchange(e).String())
	}

	for _, s := range r.shards {
		s.mu.Lock()
		for e := range snap.Exchanges {
			snap.Exchanges[e].merge(s.metrics[e])
		}
		snap.SessionsStarted += s.sessionsStarted
		snap.SessionsCompleted += s.sessionsCompleted
		for _, sess := range s.sessions {
			if sess.state == stateActive {
				snap.SessionsOnline++
			}
		}
		for _, l := range []*link{s.auth, s.acct} {
			if l != nil {
				snap.Outstanding += radius.IDSpace - l.ids.Available()
			}
		}
		s.mu.Unlock()
	}

	if r.da != nil {
		snap.DynAuth = r.da.snapshot()
	}

	var sent, ok, lost uint64
	for _, m := range snap.Exchanges {
		sent += m.Sent
		ok += m.Answered()
		lost += m.Timeouts
	}

	r.mu.Lock()
	if dt := (snap.Elapsed - r.prevAt).Seconds(); dt > 0 {
		snap.SetupRate = float64(snap.Access().Sent-r.prevSent) / dt
		snap.AnswerRate = float64(ok-r.prevOK) / dt
		snap.LossRate = float64(lost-r.prevLost) / dt
	}
	r.prevAt, r.prevSent, r.prevOK, r.prevLost = snap.Elapsed, snap.Access().Sent, ok, lost
	r.mu.Unlock()

	return snap
}
