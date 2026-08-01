package engine

import (
	"fmt"
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
}

// newSession prepares a subscriber that has not yet authenticated.
func newSession(index uint32, seq uint64, cfg *Config, runID uint32) *session {
	return &session{
		index:    index,
		seq:      seq,
		state:    stateNew,
		queueIdx: -1,
		username: expandUsername(cfg.Username, seq),
		// Unique per run and stable for the life of the session, which is
		// what a server keys its session table on.
		acctSessionID: fmt.Sprintf("%08X-%08X", runID, seq),
		interim:       cfg.InterimInterval,
	}
}

// expandUsername substitutes the session number into a template.
func expandUsername(tmpl string, seq uint64) string {
	if !strings.Contains(tmpl, "{n}") {
		return tmpl
	}
	return strings.ReplaceAll(tmpl, "{n}", strconv.FormatUint(seq, 10))
}

// buildAccess fills in an Access-Request for this subscriber.
func (s *session) buildAccess(p *radius.Packet, cfg *Config, rng interface{ UintN(uint) uint }) error {
	p.AddString(radius.AttrUserName, s.username)

	switch cfg.Auth {
	case AuthCHAP:
		ident := uint8(rng.UintN(256))
		// With no CHAP-Challenge attribute the request authenticator serves
		// as the challenge, per RFC 2865 section 5.3.
		resp := radius.CHAPPassword(ident, []byte(cfg.Password), p.Authenticator[:])
		p.Add(radius.AttrCHAPPassword, resp[:])
	default:
		enc, err := radius.EncryptUserPassword(nil, []byte(cfg.Password), cfg.Secret, p.Authenticator)
		if err != nil {
			return err
		}
		p.Add(radius.AttrUserPassword, enc)
	}

	s.addCommon(p, cfg)
	p.AddString(radius.AttrAcctSessionID, s.acctSessionID)
	p.AddUint32(radius.AttrServiceType, 2)    // Framed-User
	p.AddUint32(radius.AttrFramedProtocol, 1) // PPP
	return nil
}

// buildAcct fills in an Accounting-Request of the given status type.
func (s *session) buildAcct(p *radius.Packet, cfg *Config, status uint32, now time.Time, cause uint32) {
	p.AddString(radius.AttrUserName, s.username)
	p.AddString(radius.AttrAcctSessionID, s.acctSessionID)
	p.AddUint32(radius.AttrAcctStatusType, status)
	p.AddUint32(radius.AttrAcctAuthentic, 1) // RADIUS

	if len(s.class) > 0 {
		p.Add(radius.AttrClass, s.class)
	}
	if s.hasIP {
		p.Add(radius.AttrFramedIPAddress, s.framedIP[:])
	}
	s.addCommon(p, cfg)

	if status == acctStart {
		return
	}

	elapsed := now.Sub(s.onlineAt)
	if elapsed < 0 {
		elapsed = 0
	}
	p.AddUint32(radius.AttrAcctSessionTime, uint32(elapsed.Seconds()))

	in, out := s.octets(cfg, elapsed)
	// Octet counters are 32 bits wide, so anything past four gibibytes is
	// carried in the matching Gigawords attribute. Plenty of accounting
	// pipelines mishandle that rollover, which is exactly why a load tool
	// should reproduce it rather than quietly staying under the limit.
	p.AddUint32(radius.AttrAcctInputOctets, uint32(in))
	p.AddUint32(radius.AttrAcctOutputOctets, uint32(out))
	if hi := uint32(in >> 32); hi > 0 {
		p.AddUint32(radius.AttrAcctInputGigawords, hi)
	}
	if hi := uint32(out >> 32); hi > 0 {
		p.AddUint32(radius.AttrAcctOutputGigawords, hi)
	}
	p.AddUint32(radius.AttrAcctInputPackets, uint32(in/1400))
	p.AddUint32(radius.AttrAcctOutputPackets, uint32(out/1400))

	if status == acctStop {
		if cause == 0 {
			cause = cfg.terminateCause()
		}
		p.AddUint32(radius.AttrAcctTerminateCause, cause)
	}
}

// octets returns the traffic this session should have accumulated.
func (s *session) octets(cfg *Config, elapsed time.Duration) (in, out uint64) {
	secs := elapsed.Seconds()
	in = cfg.TrafficUp + uint64(secs*float64(cfg.TrafficUp))
	out = cfg.TrafficDown + uint64(secs*float64(cfg.TrafficDown))
	return in, out
}

// addCommon adds the attributes a BNG would put on every packet for a
// subscriber, regardless of which exchange it is.
func (s *session) addCommon(p *radius.Packet, cfg *Config) {
	if cfg.NASIdentifier != "" {
		p.AddString(radius.AttrNASIdentifier, cfg.NASIdentifier)
	}
	if cfg.NASIPAddress != nil {
		p.Add(radius.AttrNASIPAddress, cfg.NASIPAddress)
	}
	if cfg.NASPortType > 0 {
		p.AddUint32(radius.AttrNASPortType, cfg.NASPortType)
	}
	if cfg.CallingStationID != "" {
		p.AddString(radius.AttrCallingStationID, expandUsername(cfg.CallingStationID, s.seq))
	}
	if cfg.NASPortID != "" {
		p.AddString(radius.AttrNASPortID, expandUsername(cfg.NASPortID, s.seq))
	}
	p.AddUint32(radius.AttrNASPort, uint32(s.seq))
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
