package engine

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// Error causes from RFC 5176 section 3.5.
const (
	errSessionNotFound            = 503
	errResourcesUnavailable       = 506
	errAdministrativelyProhibited = 501
)

// DynAuth counts the server-initiated requests this run answered.
//
// These arrive rather than being sent, so they are counted separately from the
// exchanges a session produces. What an operator wants to know here is whether
// a bulk policy change or a mass disconnect actually reached every subscriber,
// which means the requests that could not be matched matter as much as the
// ones that could.
type DynAuth struct {
	DisconnectRequests uint64
	DisconnectACKs     uint64
	DisconnectNAKs     uint64
	CoARequests        uint64
	CoAACKs            uint64
	CoANAKs            uint64

	// Unverified counts requests whose authenticator did not check out,
	// which means the sender used a different shared secret.
	Unverified uint64
	Malformed  uint64
}

// Total returns every server-initiated packet seen, including the ones that
// could not be acted on.
func (d DynAuth) Total() uint64 {
	return d.DisconnectRequests + d.CoARequests + d.Unverified + d.Malformed
}

// sessionRef locates a live session.
type sessionRef struct {
	shard int
	index uint32
}

// registry maps a session's Acct-Session-Id to the shard that owns it.
//
// A server sending a Disconnect-Request knows the session only by the
// identifiers it was given in accounting, so this is the index that has to
// exist for dynamic authorisation to work at all.
type registry struct {
	mu     sync.RWMutex
	byAcct map[string]sessionRef
}

func newRegistry() *registry {
	return &registry{byAcct: make(map[string]sessionRef)}
}

func (r *registry) add(acctID string, ref sessionRef) {
	r.mu.Lock()
	r.byAcct[acctID] = ref
	r.mu.Unlock()
}

func (r *registry) remove(acctID string) {
	r.mu.Lock()
	delete(r.byAcct, acctID)
	r.mu.Unlock()
}

func (r *registry) lookup(acctID string) (sessionRef, bool) {
	r.mu.RLock()
	ref, ok := r.byAcct[acctID]
	r.mu.RUnlock()
	return ref, ok
}

// coaEvent is a server-initiated change handed to the shard that owns the
// session, so the session is only ever mutated by its owner.
type coaEvent struct {
	index      uint32
	disconnect bool

	// interim and sessionTimeout are zero when the request did not carry
	// them, meaning leave the session's current values alone.
	interim        time.Duration
	sessionTimeout time.Duration
}

// dynauth answers CoA-Request and Disconnect-Request on behalf of the
// simulated access node.
type dynauth struct {
	conn   *net.UDPConn
	secret []byte
	reg    *registry
	shards []*shard

	enc *radius.Encoder

	mu      sync.Mutex
	metrics DynAuth
}

func newDynAuth(addr string, secret []byte, reg *registry) (*dynauth, error) {
	udp, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("engine: resolving %s: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udp)
	if err != nil {
		return nil, fmt.Errorf("engine: listening on %s: %w", addr, err)
	}
	return &dynauth{
		conn:   conn,
		secret: secret,
		reg:    reg,
		enc:    radius.NewEncoder(secret),
	}, nil
}

func (d *dynauth) close() {
	if d != nil && d.conn != nil {
		_ = d.conn.Close()
	}
}

func (d *dynauth) snapshot() DynAuth {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.metrics
}

// serve answers requests until ctx is cancelled or the socket is closed.
func (d *dynauth) serve(ctx context.Context) {
	buf := make([]byte, radius.MaxPacketLen)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = d.conn.SetReadDeadline(time.Now().Add(sweepInterval))

		n, from, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			return
		}
		d.handle(buf[:n], from)
	}
}

func (d *dynauth) handle(req []byte, from *net.UDPAddr) {
	if len(req) < radius.HeaderLen {
		d.mu.Lock()
		d.metrics.Malformed++
		d.mu.Unlock()
		return
	}

	// Verify before anything else. An unauthenticated Disconnect-Request is
	// how an attacker would knock subscribers offline, so a packet that does
	// not check out is counted and dropped rather than answered: replying
	// would confirm to the sender that the address is live.
	if err := radius.VerifyRequest(req, d.secret); err != nil {
		d.mu.Lock()
		d.metrics.Unverified++
		d.mu.Unlock()
		return
	}

	p, err := radius.Decode(req)
	if err != nil {
		d.mu.Lock()
		d.metrics.Malformed++
		d.mu.Unlock()
		return
	}

	var ackCode, nakCode radius.Code
	disconnect := false
	switch p.Code {
	case radius.CodeDisconnectRequest:
		ackCode, nakCode, disconnect = radius.CodeDisconnectACK, radius.CodeDisconnectNAK, true
		d.mu.Lock()
		d.metrics.DisconnectRequests++
		d.mu.Unlock()
	case radius.CodeCoARequest:
		ackCode, nakCode = radius.CodeCoAACK, radius.CodeCoANAK
		d.mu.Lock()
		d.metrics.CoARequests++
		d.mu.Unlock()
	default:
		d.mu.Lock()
		d.metrics.Malformed++
		d.mu.Unlock()
		return
	}

	acctID := string(p.Get(radius.AttrAcctSessionID))
	if acctID == "" {
		d.reply(p, from, nakCode, errAdministrativelyProhibited, disconnect)
		return
	}
	ref, ok := d.reg.lookup(acctID)
	if !ok || ref.shard >= len(d.shards) {
		// The session has already gone, which for a mass disconnect is the
		// single most useful thing to surface: the request arrived after the
		// subscriber had left.
		d.reply(p, from, nakCode, errSessionNotFound, disconnect)
		return
	}

	ev := coaEvent{index: ref.index, disconnect: disconnect}
	if v, ok := p.GetUint32(radius.AttrAcctInterimInterval); ok && v > 0 {
		ev.interim = time.Duration(v) * time.Second
	}
	if v, ok := p.GetUint32(radius.AttrSessionTimeout); ok {
		ev.sessionTimeout = time.Duration(v) * time.Second
	}

	if !d.shards[ref.shard].submitCoA(ev) {
		d.reply(p, from, nakCode, errResourcesUnavailable, disconnect)
		return
	}
	d.reply(p, from, ackCode, 0, disconnect)
}

// reply answers a request. cause is included as Error-Cause when non-zero,
// which is how RFC 5176 says a NAK explains itself.
func (d *dynauth) reply(req *radius.Packet, to *net.UDPAddr, code radius.Code, cause uint32, disconnect bool) {
	resp := &radius.Packet{Code: code, Identifier: req.Identifier}
	if cause > 0 {
		resp.AddUint32(radius.AttrErrorCause, cause)
	}

	d.mu.Lock()
	wire, err := d.enc.EncodeResponse(resp, req.Authenticator)
	if err == nil {
		_, err = d.conn.WriteToUDP(wire, to)
	}
	if err != nil {
		d.metrics.Malformed++
		d.mu.Unlock()
		return
	}

	switch {
	case disconnect && cause == 0:
		d.metrics.DisconnectACKs++
	case disconnect:
		d.metrics.DisconnectNAKs++
	case cause == 0:
		d.metrics.CoAACKs++
	default:
		d.metrics.CoANAKs++
	}
	d.mu.Unlock()
}
