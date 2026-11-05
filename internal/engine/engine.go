// Package engine drives RADIUS load against a server and measures the result.
package engine

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync/atomic"
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
	// value is the spec's value with its placeholder already located, so a
	// templated attribute is not searched again for every subscriber.
	value template
}

// Auth selects how a subscriber proves its identity.
type Auth string

// Supported authentication methods.
//
// PPPoE subscribers authenticate with PAP or CHAP; a server that stores hashed
// passwords can only support PAP, because CHAP requires the cleartext.
//
// IPoE subscribers do not authenticate at all. The access node has no
// credential to present, so it sends a fixed string in User-Password purely to
// satisfy the requirement that an Access-Request carry one, and the server
// identifies the subscriber from the line instead. The value is a service
// password configured on the node, the same for every subscriber, and treating
// it as a per-subscriber credential would misrepresent what is on the wire.
const (
	AuthPAP  Auth = "pap"
	AuthCHAP Auth = "chap"
	AuthIPoE Auth = "ipoe"
)

// Shape is how offered load varies over a run.
type Shape string

// Load shapes.
const (
	// ShapeSteady holds the rate flat.
	ShapeSteady Shape = "steady"
	// ShapeRamp climbs from nothing to the rate across the run, which is how
	// the knee is found: the point where latency turns up is the capacity.
	ShapeRamp Shape = "ramp"
	// ShapeBurst alternates between the rate and a fraction of it, which is
	// what catches a server that copes with load but not with change.
	ShapeBurst Shape = "burst"
	// ShapeStampede offers everything at once, the shape of a reload where
	// every subscriber on a node re-authenticates together.
	ShapeStampede Shape = "stampede"
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

	// TrafficUp and TrafficDown are the octets per second a session claims to
	// have carried, which drives the accounting counters. When the matching
	// Max is higher, each session draws its own rate from that range, so an
	// estate looks like a spread of subscribers rather than one repeated.
	TrafficUp      uint64
	TrafficUpMax   uint64
	TrafficDown    uint64
	TrafficDownMax uint64

	// RejectRate is the fraction of sessions, from 0 to 1, given a username
	// the server will not know. Rejection is often a different and slower
	// path than acceptance, so a platform that is fine until a line card
	// flaps and thousands of stale credentials arrive at once will only show
	// that under a load that contains some.
	RejectRate float64
	// RejectUsername is the template for those sessions. Empty prefixes the
	// normal username with something no realm will match.
	RejectUsername string

	// Shape is how the offered rate varies over the run.
	Shape Shape
	// BurstPeriod is one full cycle of ShapeBurst, and BurstLow the fraction
	// of the rate offered during the quiet half.
	BurstPeriod time.Duration
	BurstLow    float64

	// Links is the number of source ports to spread across, per endpoint.
	// Each carries its own identifier space, so this sets the ceiling on
	// outstanding requests at 256 per link. Zero picks a default.
	Links int
	// Timeout is how long to wait for a reply before trying again.
	Timeout time.Duration
	// Retries is how many times an unanswered request is repeated before it
	// is counted as lost. A real access node retransmits, so modelling it is
	// the faithful default; the wait doubles after each attempt, as RFC 5080
	// section 2.2.1 describes.
	Retries int

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

	// The templated strings with their placeholders located once, and the
	// password as the bytes that go on the wire, so setting a subscriber up
	// does no string searching and no conversion.
	username         template
	rejectUsername   template
	callingStationID template
	nasPortID        template
	password         []byte

	// CoAListen is the address to accept CoA-Request and Disconnect-Request
	// on, for example ":3799". Empty means the run does not present itself as
	// an access node the server can push changes to.
	CoAListen string

	// AllowMissingMessageAuthenticator accepts replies that carry no
	// Message-Authenticator. Every Access-Request this sends carries one, so a
	// correct server echoes one back, and a reply without one is verifiable
	// only by an MD5 digest that CVE-2024-3596 shows can be forged on the path.
	// Set this only to test a peer that deliberately omits it.
	AllowMissingMessageAuthenticator bool

	// Seed makes a run reproducible. Zero draws a fresh seed.
	Seed uint64
}

// linksPerCPU and maxDefaultLinks set the default source port count.
//
// A link parks between replies far more than it works, so one per core leaves
// most of the machine idle: on a 24 thread desktop, doubling the links from 16
// to 48 lifts authentication throughput by about a quarter, and it keeps
// climbing well past that. The cap is where the return per link has flattened
// enough that the sockets, their buffers and their goroutines stop paying for
// themselves. A run that wants more can say so with -links.
const (
	linksPerCPU     = 2
	maxDefaultLinks = 64
)

// MaxLinks is the most source ports a run will open per endpoint.
//
// Every link costs two sockets and their buffers, so a large number is a real
// request for memory and file descriptors; asking for an absurd one is how a
// run brings down the machine it is driven from rather than the server it is
// aimed at. The ceiling is well above any rate a generator can usefully offer
// and is here to make a typo or a hostile caller fail cleanly instead.
const MaxLinks = 4096

func (c *Config) links() int {
	if c.Links > 0 {
		if c.Links > MaxLinks {
			return MaxLinks
		}
		return c.Links
	}
	n := runtime.NumCPU() * linksPerCPU
	if n > maxDefaultLinks {
		n = maxDefaultLinks
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

// maxRetryInterval caps the exponential backoff so a long run cannot end up
// waiting minutes between attempts.
const maxRetryInterval = 16 * time.Second

// totalWait is the longest a request can stay outstanding across every
// attempt, which is how long a run must keep listening after its last send.
func (c *Config) totalWait() time.Duration {
	total := c.timeout()
	for i := 1; i <= c.Retries; i++ {
		total += c.retryDelay(i + 1)
	}
	return total
}

// retryDelay returns how long to wait before attempt number n, counting the
// first transmission as attempt one.
func (c *Config) retryDelay(attempts int) time.Duration {
	d := c.timeout()
	for i := 1; i < attempts && d < maxRetryInterval; i++ {
		d *= 2
	}
	if d > maxRetryInterval {
		d = maxRetryInterval
	}
	return d
}

func (c *Config) accounting() bool { return c.AcctServer != "" }

func (c *Config) shape() Shape {
	if c.Shape == "" {
		return ShapeSteady
	}
	return c.Shape
}

func (c *Config) burstPeriod() time.Duration {
	if c.BurstPeriod <= 0 {
		return 10 * time.Second
	}
	return c.BurstPeriod
}

// rateAt is the offered setup rate at a point in the run.
//
// The pacer integrates this forward rather than assuming a fixed gap between
// sessions, which is what lets the rate vary without losing the schedule a
// request was due on. Latency is still measured against that schedule, so a
// burst the generator cannot keep up with shows as delay rather than
// disappearing.
// sessionsBy is how many sessions the whole run should have started by a point
// in it, being the integral of the offered rate.
//
// The pacer works from this cumulative demand rather than from a gap between
// sessions. A gap is the reciprocal of the rate, so a shape that starts near
// zero produces an enormous first gap and then never re-examines it, which
// leaves a ramp firing a handful of requests and stopping. Demand has no such
// singularity, and inverting it gives the moment each session was due, which
// is what latency is measured against.
func (c *Config) sessionsBy(elapsed time.Duration) float64 {
	if c.Rate <= 0 || elapsed <= 0 {
		return 0
	}
	t := elapsed.Seconds()

	switch c.shape() {
	case ShapeRamp:
		d := c.Duration.Seconds()
		if d <= 0 {
			return c.Rate * t
		}
		if t >= d {
			// The area under the climb, then the full rate afterwards.
			return c.Rate*d/2 + c.Rate*(t-d)
		}
		return c.Rate * t * t / (2 * d)

	case ShapeBurst:
		period := c.burstPeriod().Seconds()
		low := c.BurstLow
		if low < 0 {
			low = 0
		}
		if low > 1 {
			low = 1
		}
		half := period / 2
		perCycle := c.Rate*half + c.Rate*low*half

		cycles := math.Floor(t / period)
		total := cycles * perCycle
		rest := t - cycles*period
		if rest <= half {
			return total + c.Rate*rest
		}
		return total + c.Rate*half + c.Rate*low*(rest-half)

	default:
		return c.Rate * t
	}
}

// timeFor is the moment demand reached n sessions, found by bisection because
// the burst shape has no tidy inverse. Paced runs set sessions up at rates a
// server can answer, so a few dozen arithmetic steps per session costs nothing
// next to the exchange it schedules.
func (c *Config) timeFor(n float64) time.Duration {
	if c.Rate <= 0 || n <= 0 {
		return 0
	}

	// Grow an upper bound until demand passes n.
	hi := time.Second
	for c.sessionsBy(hi) < n {
		hi *= 2
		if hi > 24*time.Hour {
			return hi
		}
	}

	lo := time.Duration(0)
	for i := 0; i < 40 && hi-lo > time.Microsecond; i++ {
		mid := lo + (hi-lo)/2
		if c.sessionsBy(mid) < n {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

func (c *Config) rateAt(elapsed time.Duration) float64 {
	if c.Rate <= 0 {
		return 0 // unpaced
	}
	switch c.shape() {
	case ShapeStampede:
		return 0 // everything at once
	case ShapeRamp:
		if c.Duration <= 0 {
			return c.Rate
		}
		f := elapsed.Seconds() / c.Duration.Seconds()
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		return c.Rate * f
	case ShapeBurst:
		period := c.burstPeriod()
		low := c.BurstLow
		if low < 0 {
			low = 0
		}
		if low > 1 {
			low = 1
		}
		if math.Mod(elapsed.Seconds(), period.Seconds()) < period.Seconds()/2 {
			return c.Rate
		}
		return c.Rate * low
	default:
		return c.Rate
	}
}

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
	case c.Auth != AuthPAP && c.Auth != AuthCHAP && c.Auth != AuthIPoE:
		return fmt.Errorf("engine: unknown authentication method %q", c.Auth)
	case c.RejectRate < 0 || c.RejectRate > 1:
		return errors.New("engine: the reject rate must be between 0 and 1")
	case c.Links < 0:
		return errors.New("engine: the number of links cannot be negative")
	case c.Links > MaxLinks:
		return fmt.Errorf("engine: %d links is more than the %d this will open; "+
			"a run is limited by the server long before this", c.Links, MaxLinks)
	case c.Rate < 0:
		// Zero means unpaced, which is deliberate. A negative rate is not a
		// slower version of that, it is a mistake, and it would otherwise be
		// silently read as unpaced too.
		return errors.New("engine: the rate cannot be negative; leave it at zero to run unpaced")
	case c.Retries < 0:
		return errors.New("engine: the retry count cannot be negative")
	case c.TrafficUpMax > 0 && c.TrafficUpMax < c.TrafficUp:
		return errors.New("engine: the upstream traffic maximum is below its minimum")
	case c.TrafficDownMax > 0 && c.TrafficDownMax < c.TrafficDown:
		return errors.New("engine: the downstream traffic maximum is below its minimum")
	case c.Shape == ShapeRamp && c.Duration <= 0:
		return errors.New("engine: a ramp needs a duration to climb across")
	case c.accounting() && c.InterimInterval < 0:
		return errors.New("engine: interim interval cannot be negative")
	}
	c.username = newTemplate(c.Username)
	c.rejectUsername = newTemplate(c.RejectUsername)
	c.callingStationID = newTemplate(c.CallingStationID)
	c.nasPortID = newTemplate(c.NASPortID)
	c.password = []byte(c.Password)

	return c.prepareAttributes()
}

// prepareAttributes encodes the constant attribute values once and checks the
// templated ones, so a value that cannot be encoded is reported at startup
// rather than silently dropped from every packet.
func (c *Config) prepareAttributes() error {
	c.prepared = make([]extraAttr, 0, len(c.Attributes))
	for _, spec := range c.Attributes {
		e := extraAttr{spec: spec, value: newTemplate(spec.Value)}
		if !e.value.has {
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

	// Retransmits counts repeated attempts, not requests. A high count means
	// first attempts are being lost, which is invisible in the success rate
	// once the retries have covered it up.
	Retransmits uint64
	Malformed   uint64
	Unmatched   uint64

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
	m.Retransmits += o.Retransmits
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

// counter hands out session sequence numbers across shards.
//
// Every shard takes from it for every session it offers, which at rate is the
// one piece of state the whole run shares. An atomic add keeps that from
// becoming a queue: overshooting the limit is corrected by handing back a
// refusal rather than by holding a lock to prevent it.
type counter struct {
	next  atomic.Uint64
	limit uint64
}

// take reserves the next sequence number. It reports false once the limit is
// reached.
func (c *counter) take() (uint64, bool) {
	n := c.next.Add(1)
	if c.limit > 0 && n > c.limit {
		return 0, false
	}
	return n, true
}

// remaining reports whether any sequence numbers are left.
func (c *counter) remaining() bool {
	return c.limit == 0 || c.next.Load() < c.limit
}
