package radius

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrNoReply is returned when every attempt timed out with nothing received.
var ErrNoReply = errors.New("radius: no reply from server")

// ErrUnverifiedReply is returned when replies arrived but none could be
// verified. This is almost always a shared secret mismatch, and reporting it
// separately from a timeout is the difference between a usable diagnosis and
// a shrug: the two have entirely different remedies.
var ErrUnverifiedReply = errors.New("radius: replies received but none verified, check the shared secret")

// Client performs single RADIUS exchanges over UDP.
//
// This is the straightforward synchronous path used by one-shot commands. It
// deliberately does no identifier management beyond the packet it is given,
// because a load run needs identifiers allocated across many sockets at once
// and does its own sending.
type Client struct {
	// Addr is the server, as host:port.
	Addr string
	// Secret is the shared secret.
	Secret []byte
	// Timeout bounds each individual attempt. Zero means three seconds.
	Timeout time.Duration
	// Retries is the number of additional attempts after the first. Zero
	// means two, giving three attempts in total.
	Retries int
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 3 * time.Second
	}
	return c.Timeout
}

func (c *Client) retries() int {
	if c.Retries <= 0 {
		return 2
	}
	return c.Retries
}

// Exchange sends p and returns the reply.
//
// Retransmissions reuse the identical encoded bytes, so a server that has
// already answered can recognise the duplicate rather than treating it as a
// new request. The reply's Response Authenticator and Message-Authenticator
// are both verified before it is returned; a reply that fails verification is
// discarded and the exchange continues waiting, because an attacker able to
// inject packets should not be able to end a legitimate exchange.
func (c *Client) Exchange(ctx context.Context, p *Packet) (*Packet, error) {
	wire, err := p.Encode(nil, c.Secret)
	if err != nil {
		return nil, err
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", c.Addr)
	if err != nil {
		return nil, fmt.Errorf("radius: dialling %s: %w", c.Addr, err)
	}
	defer func() { _ = conn.Close() }()

	buf := make([]byte, MaxPacketLen)
	unverified := 0

	for attempt := 0; attempt <= c.retries(); attempt++ {
		if _, err := conn.Write(wire); err != nil {
			return nil, fmt.Errorf("radius: sending to %s: %w", c.Addr, err)
		}

		deadline := time.Now().Add(c.timeout())
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}

		for {
			n, err := conn.Read(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break // next attempt
				}
				return nil, fmt.Errorf("radius: reading from %s: %w", c.Addr, err)
			}

			reply := buf[:n]
			if n < HeaderLen || reply[1] != p.Identifier {
				continue // not ours
			}
			if err := Verify(reply, p.Authenticator, c.Secret); err != nil {
				// Forged, corrupt, or signed with a different secret. Keep
				// waiting rather than failing, so an injected packet cannot
				// end a legitimate exchange, but remember that it happened.
				unverified++
				continue
			}
			return Decode(reply)
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if unverified > 0 {
		return nil, fmt.Errorf("%w (%d discarded)", ErrUnverifiedReply, unverified)
	}
	return nil, fmt.Errorf("%w after %d attempts", ErrNoReply, c.retries()+1)
}
