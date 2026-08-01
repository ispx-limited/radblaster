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
	// latency is measured against. sentAt is when it actually did.
	dueAt  time.Time
	sentAt time.Time
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
	ids     *radius.IDPool
	pending [radius.IDSpace]pending
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
	return &link{
		conn: conn,
		enc:  radius.NewEncoder(secret),
		ids:  radius.NewIDPool(r),
	}, nil
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
