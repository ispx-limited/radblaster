package engine

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// pending is one outstanding request on a link.
type pending struct {
	active   bool
	session  uint32
	exchange Exchange
	auth     [radius.AuthenticatorLen]byte

	// dueAt is when the request was scheduled to go out, which is what
	// latency is measured against. sentAt is when the first attempt actually
	// went out; retransmissions deliberately do not move it, because what a
	// subscriber waited for is the time from the original request, not from
	// whichever copy of it happened to arrive.
	dueAt  time.Time
	sentAt time.Time

	// wire holds the exact bytes sent. A retransmission repeats them rather
	// than re-encoding, so the identifier and authenticator are unchanged and
	// a server that has already answered can recognise the duplicate instead
	// of treating it as a new request.
	wire []byte

	// attempts counts transmissions including the first, and retryAt is when
	// the next one falls due.
	attempts int
	retryAt  time.Time
}

// link is one UDP socket to one server endpoint, together with the identifier
// space and outstanding-request table belonging to that source port.
//
// The RADIUS identifier is a single octet, so a link can carry at most 256
// requests at a time no matter how much work is pushed at it. That is why
// concurrency in this engine comes from opening more links rather than from
// running more goroutines.
type link struct {
	conn    *net.UDPConn
	enc     *radius.Encoder
	ver     *radius.Verifier
	rnd     *radius.Rand
	ids     *radius.IDPool
	pending [radius.IDSpace]pending

	// build assembles the next request and reply holds the last one decoded.
	// Both are reused rather than allocated per packet, which at rate is the
	// difference between measuring the server and measuring the collector.
	build requestBuilder
	reply radius.Packet
}

func newLink(server string, secret []byte, r *rand.Rand) (*link, error) {
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, fmt.Errorf("engine: resolving %s: %w", server, err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("engine: opening socket to %s: %w", server, err)
	}

	// A link's replies arrive in bursts while its goroutine is busy with the
	// one before, and the default socket buffer is small enough that a run at
	// rate overflows it. A dropped reply is counted as a timeout, so the tool
	// would be reporting its own loss as the server's. Best effort: the kernel
	// clamps this to net.core.rmem_max, and a smaller buffer is still a working
	// run rather than a failure to start.
	_ = conn.SetReadBuffer(linkBufferBytes)
	_ = conn.SetWriteBuffer(linkBufferBytes)

	l := &link{
		conn: conn,
		enc:  radius.NewEncoder(secret),
		ver:  radius.NewVerifier(secret),
		rnd:  radius.NewRand(),
		ids:  radius.NewIDPool(r),
	}
	l.build.init()
	return l, nil
}

// linkBufferBytes is the socket buffer asked for on each link, in each
// direction. Large enough to absorb a burst of replies without dropping any,
// small enough that a run with hundreds of links does not ask the kernel for an
// unreasonable amount of memory.
const linkBufferBytes = 512 << 10

// builderArena is how much attribute value storage one request is built in.
// Comfortably more than a subscriber's worth of attributes, so the arena is
// sized once and never grown.
const builderArena = 1024

// requestBuilder assembles a request into storage that is reused for the next
// one.
//
// Attribute values are written into a single arena rather than allocated
// individually, and the packet keeps its attribute slice between requests. A
// value handed out stays readable even if a later append grows the arena,
// because the slice given to the packet still refers to the storage the value
// was written into.
type requestBuilder struct {
	pkt  radius.Packet
	vals []byte
}

func (b *requestBuilder) init() {
	b.vals = make([]byte, 0, builderArena)
	b.pkt.Attributes = make([]radius.Attribute, 0, 24)
}

// reset starts a fresh request of the given code and identifier.
func (b *requestBuilder) reset(code radius.Code, id uint8) *radius.Packet {
	b.pkt.Reset(code, id)
	b.vals = b.vals[:0]
	return &b.pkt
}

// value returns the arena position a value is about to be written at.
func (b *requestBuilder) value() int { return len(b.vals) }

// commit records everything written to the arena since start as an attribute.
func (b *requestBuilder) commit(vendor uint32, t radius.AttrType, start int) {
	v := b.vals[start:len(b.vals):len(b.vals)]
	b.pkt.Attributes = append(b.pkt.Attributes, radius.Attribute{
		Type:   t,
		Vendor: vendor,
		Value:  v,
	})
}

func (b *requestBuilder) add(t radius.AttrType, v []byte) {
	start := b.value()
	b.vals = append(b.vals, v...)
	b.commit(0, t, start)
}

func (b *requestBuilder) addString(t radius.AttrType, v string) {
	start := b.value()
	b.vals = append(b.vals, v...)
	b.commit(0, t, start)
}

func (b *requestBuilder) addUint32(t radius.AttrType, v uint32) {
	start := b.value()
	b.vals = append(b.vals, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	b.commit(0, t, start)
}

func (l *link) close() {
	if l != nil && l.conn != nil {
		_ = l.conn.Close()
	}
}

// available reports whether the link can carry another request.
func (l *link) available() bool { return l.ids.Available() > 0 }

// isTimeout reports whether err is a read deadline expiring rather than a real
// failure.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
