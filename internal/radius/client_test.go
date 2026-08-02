package radius

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// responder is a controllable RADIUS server used to drive the client through
// each of its outcomes without depending on a real implementation.
type responder struct {
	conn *net.UDPConn
	// replySecret signs the reply. Setting it to something other than the
	// client's secret simulates a shared secret mismatch.
	replySecret []byte
	// ignoreFirst drops this many requests before answering, to exercise
	// retransmission.
	ignoreFirst int32
	// identifierOverride, when non-negative, replaces the reply identifier.
	identifierOverride int
	// silent drops every request.
	silent bool
	// omitMessageAuth answers without a Message-Authenticator, as a server
	// predating the CVE-2024-3596 guidance does.
	omitMessageAuth bool

	received atomic.Int32
}

func newResponder(t *testing.T, r *responder) *responder {
	t.Helper()

	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	r.conn = conn

	go r.serve()
	t.Cleanup(func() { _ = conn.Close() })
	return r
}

func (r *responder) addr() string { return r.conn.LocalAddr().String() }

func (r *responder) serve() {
	buf := make([]byte, MaxPacketLen)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return // closed
		}
		r.received.Add(1)
		if r.silent {
			continue
		}
		if r.ignoreFirst > 0 {
			r.ignoreFirst--
			continue
		}
		if n < HeaderLen {
			continue
		}

		var reqAuth [AuthenticatorLen]byte
		copy(reqAuth[:], buf[4:HeaderLen])

		id := buf[1]
		if r.identifierOverride >= 0 {
			id = byte(r.identifierOverride)
		}

		reply := []byte{byte(CodeAccessAccept), id, 0, 0}
		reply = append(reply, reqAuth[:]...)
		reply = append(reply, byte(AttrFramedIPAddress), 6, 192, 0, 2, 10)

		// A server that answers an Access-Request carrying a
		// Message-Authenticator echoes one back, so that is the default here.
		msgAuthOff := -1
		if !r.omitMessageAuth {
			reply = append(reply, byte(AttrMessageAuthenticator), messageAuthenticatorLen)
			msgAuthOff = len(reply)
			reply = append(reply, zeros[:]...)
		}
		binary.BigEndian.PutUint16(reply[2:4], uint16(len(reply)))

		if msgAuthOff >= 0 {
			mac := hmac.New(md5.New, r.replySecret)
			mac.Write(reply)
			copy(reply[msgAuthOff:msgAuthOff+AuthenticatorLen], mac.Sum(nil))
		}
		signReply(reply, r.replySecret)

		_, _ = r.conn.WriteToUDP(reply, from)
	}
}

func TestClientExchangeSucceeds(t *testing.T) {
	r := newResponder(t, &responder{replySecret: testSecret, identifierOverride: -1})
	c := &Client{Addr: r.addr(), Secret: testSecret, Timeout: time.Second, Retries: 1}

	p, err := New(CodeAccessRequest, 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.AddString(AttrUserName, "subscriber@example.net")

	reply, err := c.Exchange(context.Background(), p)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if reply.Code != CodeAccessAccept {
		t.Errorf("code = %v, want Access-Accept", reply.Code)
	}
	if ip := reply.Get(AttrFramedIPAddress); len(ip) != 4 || ip[3] != 10 {
		t.Errorf("Framed-IP-Address = %v, want 192.0.2.10", ip)
	}
}

// TestClientReportsSecretMismatch is the distinction that makes a failure
// actionable: a reply signed with the wrong secret must not be reported as
// silence, because the remedies are entirely different.
func TestClientReportsSecretMismatch(t *testing.T) {
	r := newResponder(t, &responder{replySecret: []byte("a different secret"), identifierOverride: -1})
	c := &Client{Addr: r.addr(), Secret: testSecret, Timeout: 200 * time.Millisecond, Retries: 1}

	p, err := New(CodeAccessRequest, 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Exchange(context.Background(), p)
	if !errors.Is(err, ErrUnverifiedReply) {
		t.Errorf("err = %v, want ErrUnverifiedReply", err)
	}
	if errors.Is(err, ErrNoReply) {
		t.Error("a secret mismatch was reported as silence")
	}
}

func TestClientReportsSilence(t *testing.T) {
	r := newResponder(t, &responder{silent: true, replySecret: testSecret, identifierOverride: -1})
	c := &Client{Addr: r.addr(), Secret: testSecret, Timeout: 100 * time.Millisecond, Retries: 1}

	p, err := New(CodeAccessRequest, 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.Exchange(context.Background(), p); !errors.Is(err, ErrNoReply) {
		t.Errorf("err = %v, want ErrNoReply", err)
	}
	if got := r.received.Load(); got != 2 {
		t.Errorf("server saw %d requests, want 2 (initial plus one retransmission)", got)
	}
}

func TestClientRetransmits(t *testing.T) {
	r := newResponder(t, &responder{ignoreFirst: 1, replySecret: testSecret, identifierOverride: -1})
	c := &Client{Addr: r.addr(), Secret: testSecret, Timeout: 200 * time.Millisecond, Retries: 2}

	p, err := New(CodeAccessRequest, 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.Exchange(context.Background(), p); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := r.received.Load(); got != 2 {
		t.Errorf("server saw %d requests, want 2", got)
	}
}

// TestClientIgnoresForeignIdentifier guards against accepting another
// exchange's reply, which at load is the difference between correct results
// and quietly correlated nonsense.
func TestClientIgnoresForeignIdentifier(t *testing.T) {
	r := newResponder(t, &responder{replySecret: testSecret, identifierOverride: 200})
	c := &Client{Addr: r.addr(), Secret: testSecret, Timeout: 150 * time.Millisecond, Retries: 0}

	p, err := New(CodeAccessRequest, 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.Exchange(context.Background(), p); !errors.Is(err, ErrNoReply) {
		t.Errorf("err = %v, want ErrNoReply", err)
	}
}

func TestClientHonoursContextCancellation(t *testing.T) {
	r := newResponder(t, &responder{silent: true, replySecret: testSecret, identifierOverride: -1})
	c := &Client{Addr: r.addr(), Secret: testSecret, Timeout: 5 * time.Second, Retries: 5}

	p, err := New(CodeAccessRequest, 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := c.Exchange(ctx, p); err == nil {
		t.Fatal("expected an error once the context expired")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("Exchange took %s, want it to stop when the context expired", elapsed)
	}
}

// signReply writes the Response Authenticator a server would compute.
func signReply(reply, secret []byte) {
	h := md5.New()
	h.Write(reply)
	h.Write(secret)
	copy(reply[4:HeaderLen], h.Sum(nil))
}

// A reply verifiable only by its MD5 Response Authenticator is what
// CVE-2024-3596 forges, so one that carries no Message-Authenticator is
// refused rather than quietly accepted.
func TestClientRefusesReplyWithoutMessageAuthenticator(t *testing.T) {
	r := newResponder(t, &responder{
		replySecret:        testSecret,
		identifierOverride: -1,
		omitMessageAuth:    true,
	})
	c := &Client{Addr: r.addr(), Secret: testSecret, Timeout: 300 * time.Millisecond, Retries: 1}

	p, err := New(CodeAccessRequest, 9)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Exchange(context.Background(), p); !errors.Is(err, ErrNoMessageAuth) {
		t.Fatalf("err = %v, want ErrNoMessageAuth", err)
	}
}

// The refusal is a default, not a rule: testing a peer that omits it is a
// legitimate thing to want.
func TestClientCanAcceptReplyWithoutMessageAuthenticator(t *testing.T) {
	r := newResponder(t, &responder{
		replySecret:        testSecret,
		identifierOverride: -1,
		omitMessageAuth:    true,
	})
	c := &Client{
		Addr: r.addr(), Secret: testSecret, Timeout: time.Second, Retries: 1,
		AllowMissingMessageAuthenticator: true,
	}

	p, err := New(CodeAccessRequest, 10)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reply, err := c.Exchange(context.Background(), p)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if reply.Code != CodeAccessAccept {
		t.Errorf("code = %v, want Access-Accept", reply.Code)
	}
}

// A tampered Message-Authenticator must not pass, which is the property the
// whole requirement rests on.
func TestVerifierRejectsForgedMessageAuthenticator(t *testing.T) {
	var reqAuth [AuthenticatorLen]byte
	p, err := New(CodeAccessRequest, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reqAuth = p.Authenticator

	reply := []byte{byte(CodeAccessAccept), 3, 0, 0}
	reply = append(reply, reqAuth[:]...)
	reply = append(reply, byte(AttrMessageAuthenticator), messageAuthenticatorLen)
	off := len(reply)
	reply = append(reply, zeros[:]...)
	binary.BigEndian.PutUint16(reply[2:4], uint16(len(reply)))

	mac := hmac.New(md5.New, testSecret)
	mac.Write(reply)
	copy(reply[off:off+AuthenticatorLen], mac.Sum(nil))
	signReply(reply, testSecret)

	v := NewVerifier(testSecret)
	v.RequireMessageAuthenticator(true)
	if err := v.Verify(reply, reqAuth); err != nil {
		t.Fatalf("a correctly signed reply should verify: %v", err)
	}

	reply[off] ^= 0x01
	if err := v.Verify(reply, reqAuth); !errors.Is(err, ErrBadMessageAuth) {
		t.Fatalf("err = %v, want ErrBadMessageAuth", err)
	}
}
