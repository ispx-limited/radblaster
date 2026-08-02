package engine

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// sessionState is where a subscriber session has got to in its lifecycle.
type sessionState uint8

const (
	stateNew            sessionState = iota // needs an Access-Request
	stateAuthenticating                     // Access-Request sent
	stateAcctStart                          // authenticated, needs Accounting-Start
	stateActive                             // online, sending interim updates
	stateAcctStop                           // hold time elapsed, needs Accounting-Stop
	stateDone
)

// Exchange identifies a kind of request, which is the dimension results are
// grouped by. One row per exchange is what tells an operator that
// authentication is healthy while accounting is collapsing, a distinction a
// single aggregate latency figure cannot express.
type Exchange int

// The exchanges a subscriber session produces.
const (
	ExchangeAccess Exchange = iota
	ExchangeAcctStart
	ExchangeAcctInterim
	ExchangeAcctStop
	exchangeCount
)

// ExchangeNames are the display names, indexed by Exchange.
var ExchangeNames = [exchangeCount]string{
	"Access-Request",
	"Accounting-Start",
	"Interim-Update",
	"Accounting-Stop",
}

func (e Exchange) String() string {
	if e < 0 || int(e) >= len(ExchangeNames) {
		return "Exchange(" + strconv.Itoa(int(e)) + ")"
	}
	return ExchangeNames[e]
}

// session is one simulated subscriber.
type session struct {
	index uint32
	seq   uint64

	state  sessionState
	nextAt time.Time
	// queueIdx is this session's position in its shard's due queue, or -1
	// when it is not queued. Tracking it lets a disconnect remove the
	// session directly instead of searching for it.
	queueIdx int

	username      string
	acctSessionID string

	// callingStationID and nasPortID are rendered once here rather than for
	// every packet, because they depend only on the session number and a
	// subscriber that stays online sends a great many packets.
	callingStationID string
	nasPortID        string

	// class and framedIP are taken from the Access-Accept. Echoing Class
	// back in accounting is required by RFC 2865 and is a real test of the
	// whole chain, including any proxy in the path.
	class    []byte
	framedIP [4]byte
	hasIP    bool

	// interim is the update period. A server that returns
	// Acct-Interim-Interval is telling us how much accounting load it wants,
	// and honouring that per session rather than globally is what makes a
	// mixed-policy estate behave realistically.
	interim time.Duration

	onlineAt time.Time
	stopAt   time.Time

	// up and down are this subscriber's own octets per second, drawn once so
	// its counters climb at a consistent rate for its whole life.
	up   uint64
	down uint64

	// reject marks a session deliberately given an unknown username.
	reject bool

	// online mirrors membership of the shard's online tally. Keeping it as a
	// flag rather than deriving it from state means the count stays right
	// however a session leaves, including a teardown that passes through an
	// intermediate state on the way out.
	online bool
}

// newSession prepares a subscriber that has not yet authenticated.
//
// Everything that varies between subscribers is drawn here, from the shard's
// seeded stream, so a run with the same seed produces the same estate.
func newSession(index uint32, seq uint64, cfg *Config, runID uint32, rng *rand.Rand) *session {
	s := &session{
		index:    index,
		seq:      seq,
		state:    stateNew,
		queueIdx: -1,
		username: cfg.username.render(seq),
		// Unique per run and stable for the life of the session, which is
		// what a server keys its session table on.
		acctSessionID:    acctSessionID(runID, seq),
		callingStationID: cfg.callingStationID.render(seq),
		nasPortID:        cfg.nasPortID.render(seq),
		interim:          cfg.InterimInterval,
		up:               drawRate(cfg.TrafficUp, cfg.TrafficUpMax, rng),
		down:             drawRate(cfg.TrafficDown, cfg.TrafficDownMax, rng),
	}
	if cfg.RejectRate > 0 && rng.Float64() < cfg.RejectRate {
		s.reject = true
		s.username = rejectUsername(cfg, seq)
	}
	return s
}

// acctSessionID formats the Acct-Session-Id a session carries for its whole
// life. It is written by hand rather than with fmt because it is produced once
// per subscriber, and a run sets up millions of them.
func acctSessionID(runID uint32, seq uint64) string {
	var b [40]byte
	out := appendHex(b[:0], uint64(runID), 8)
	out = append(out, '-')
	out = appendHex(out, seq, 8)
	return string(out)
}

// appendHex appends v in upper case hexadecimal, zero padded to at least width
// digits.
func appendHex(dst []byte, v uint64, width int) []byte {
	const digits = "0123456789ABCDEF"
	var tmp [16]byte
	i := len(tmp)
	for {
		i--
		tmp[i] = digits[v&0xf]
		v >>= 4
		if v == 0 {
			break
		}
	}
	for n := len(tmp) - i; n < width; n++ {
		dst = append(dst, '0')
	}
	return append(dst, tmp[i:]...)
}

// drawRate picks a subscriber's traffic rate, uniformly across the range when
// one is given and fixed otherwise.
func drawRate(min, max uint64, rng *rand.Rand) uint64 {
	if max <= min {
		return min
	}
	return min + rng.Uint64N(max-min+1)
}

// rejectUsername returns a name no server should know. The prefix is
// deliberate and obvious so a rejection in a server log reads as this tool
// asking for one rather than as a real subscriber failing to get online.
func rejectUsername(cfg *Config, seq uint64) string {
	if cfg.RejectUsername != "" {
		return cfg.rejectUsername.render(seq)
	}
	return "radblaster-unknown-" + strconv.FormatUint(seq, 10) + "@invalid"
}

// template is a string that may carry {n} for the session number, with the
// search for it done once at startup rather than for every subscriber it is
// rendered for.
type template struct {
	raw string
	has bool
}

func newTemplate(s string) template {
	return template{raw: s, has: strings.Contains(s, "{n}")}
}

// render substitutes the session number, returning the template unchanged when
// it carries no placeholder.
func (t template) render(seq uint64) string {
	if !t.has {
		return t.raw
	}
	return strings.ReplaceAll(t.raw, "{n}", strconv.FormatUint(seq, 10))
}

// expandUsername substitutes the session number into a template.
func expandUsername(tmpl string, seq uint64) string {
	return newTemplate(tmpl).render(seq)
}

// buildAccess fills in an Access-Request for this subscriber.
func (s *session) buildAccess(b *requestBuilder, cfg *Config, rng interface{ UintN(uint) uint }) error {
	p := &b.pkt
	b.addString(radius.AttrUserName, s.username)

	switch cfg.Auth {
	case AuthCHAP:
		ident := uint8(rng.UintN(256))
		// With no CHAP-Challenge attribute the request authenticator serves
		// as the challenge, per RFC 2865 section 5.3.
		resp := radius.CHAPPassword(ident, cfg.password, p.Authenticator[:])
		b.add(radius.AttrCHAPPassword, resp[:])
	default:
		// PAP and IPoE put the same attribute on the wire. They differ in
		// what it means: for PPPoE it is the subscriber's password, and for
		// IPoE it is a fixed service password the access node was configured
		// with, present only because an Access-Request has to carry one.
		start := b.value()
		vals, err := radius.EncryptUserPassword(b.vals, cfg.password, cfg.Secret, p.Authenticator)
		if err != nil {
			return err
		}
		b.vals = vals
		b.commit(0, radius.AttrUserPassword, start)
	}

	s.addCommon(b, cfg)
	b.addString(radius.AttrAcctSessionID, s.acctSessionID)
	b.addUint32(radius.AttrServiceType, 2)    // Framed-User
	b.addUint32(radius.AttrFramedProtocol, 1) // PPP
	return nil
}

// buildAcct fills in an Accounting-Request of the given status type.
func (s *session) buildAcct(b *requestBuilder, cfg *Config, status uint32, now time.Time, cause uint32) {
	b.addString(radius.AttrUserName, s.username)
	b.addString(radius.AttrAcctSessionID, s.acctSessionID)
	b.addUint32(radius.AttrAcctStatusType, status)
	b.addUint32(radius.AttrAcctAuthentic, 1) // RADIUS

	if len(s.class) > 0 {
		b.add(radius.AttrClass, s.class)
	}
	if s.hasIP {
		b.add(radius.AttrFramedIPAddress, s.framedIP[:])
	}
	s.addCommon(b, cfg)

	if status == acctStart {
		return
	}

	elapsed := now.Sub(s.onlineAt)
	if elapsed < 0 {
		elapsed = 0
	}
	b.addUint32(radius.AttrAcctSessionTime, uint32(elapsed.Seconds()))

	in, out := s.octets(elapsed)
	// Octet counters are 32 bits wide, so anything past four gibibytes is
	// carried in the matching Gigawords attribute. Plenty of accounting
	// pipelines mishandle that rollover, which is exactly why a load tool
	// should reproduce it rather than quietly staying under the limit.
	b.addUint32(radius.AttrAcctInputOctets, uint32(in))
	b.addUint32(radius.AttrAcctOutputOctets, uint32(out))
	if hi := uint32(in >> 32); hi > 0 {
		b.addUint32(radius.AttrAcctInputGigawords, hi)
	}
	if hi := uint32(out >> 32); hi > 0 {
		b.addUint32(radius.AttrAcctOutputGigawords, hi)
	}
	b.addUint32(radius.AttrAcctInputPackets, uint32(in/1400))
	b.addUint32(radius.AttrAcctOutputPackets, uint32(out/1400))

	if status == acctStop {
		if cause == 0 {
			cause = cfg.terminateCause()
		}
		b.addUint32(radius.AttrAcctTerminateCause, cause)
	}
}

// octets returns the traffic this session should have accumulated.
func (s *session) octets(elapsed time.Duration) (in, out uint64) {
	secs := elapsed.Seconds()
	return s.up + uint64(secs*float64(s.up)), s.down + uint64(secs*float64(s.down))
}

// addCommon adds the attributes a BNG would put on every packet for a
// subscriber, regardless of which exchange it is.
func (s *session) addCommon(b *requestBuilder, cfg *Config) {
	if cfg.NASIdentifier != "" {
		b.addString(radius.AttrNASIdentifier, cfg.NASIdentifier)
	}
	if cfg.NASIPAddress != nil {
		b.add(radius.AttrNASIPAddress, cfg.NASIPAddress)
	}
	if cfg.NASPortType > 0 {
		b.addUint32(radius.AttrNASPortType, cfg.NASPortType)
	}
	if s.callingStationID != "" {
		b.addString(radius.AttrCallingStationID, s.callingStationID)
	}
	if s.nasPortID != "" {
		b.addString(radius.AttrNASPortID, s.nasPortID)
	}
	b.addUint32(radius.AttrNASPort, uint32(s.seq))

	for i := range cfg.prepared {
		e := &cfg.prepared[i]
		v := e.static
		if v == nil {
			// Templated, so it has to be rendered for this subscriber.
			var err error
			if v, err = e.spec.EncodeValue(e.value.render(s.seq)); err != nil {
				continue
			}
		}
		start := b.value()
		b.vals = append(b.vals, v...)
		b.commit(e.spec.Vendor, e.spec.Type, start)
	}
}

// applyAccept takes the parts of an Access-Accept the session must carry
// forward into its accounting.
func (s *session) applyAccept(reply *radius.Packet, cfg *Config) {
	if class := reply.Get(radius.AttrClass); len(class) > 0 {
		s.class = append(s.class[:0], class...)
	}
	if ip := reply.Get(radius.AttrFramedIPAddress); len(ip) == 4 {
		copy(s.framedIP[:], ip)
		s.hasIP = true
	}
	if v, ok := reply.GetUint32(radius.AttrAcctInterimInterval); ok && v > 0 {
		s.interim = time.Duration(v) * time.Second
	}
	if s.interim <= 0 {
		s.interim = cfg.InterimInterval
	}
}

// Accounting status types from RFC 2866.
const (
	acctStart   = 1
	acctStop    = 2
	acctInterim = 3
)

// Terminate causes from RFC 2866 section 5.10.
const (
	causeUserRequest = 1
	causeAdminReset  = 6
)
