package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// freePort returns a UDP port that is free right now, so a test can tell the
// engine where to listen before the run starts.
func freePort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return port
}

// waitForStart blocks until the server has seen an Accounting-Start and
// returns its Acct-Session-Id, which is how a real server would address a
// disconnect.
func waitForStart(t *testing.T, srv *acctServer) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if starts := srv.seen(acctStart); len(starts) > 0 {
			return string(starts[0].Get(radius.AttrAcctSessionID))
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no session came online in time")
	return ""
}

// sendDynAuth sends a server-initiated request and returns the reply.
func sendDynAuth(t *testing.T, addr string, code radius.Code, secret []byte, attrs ...radius.Attribute) *radius.Packet {
	t.Helper()

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()

	p := &radius.Packet{Code: code, Identifier: 17}
	p.Attributes = append(p.Attributes, attrs...)
	wire, err := p.Encode(nil, secret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := conn.Write(wire); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, radius.MaxPacketLen)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}
	if err := radius.Verify(buf[:n], p.Authenticator, secret); err != nil {
		t.Errorf("reply did not verify: %v", err)
		return nil
	}
	reply, err := radius.Decode(buf[:n])
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return reply
}

func coaConfig(t *testing.T, srv *acctServer) (Config, string) {
	t.Helper()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	cfg := sessionConfig(srv)
	cfg.CoAListen = addr
	cfg.Count = 1
	cfg.Hold = 5 * time.Second
	cfg.InterimInterval = 3 * time.Second
	cfg.Timeout = time.Second
	return cfg, addr
}

// TestDisconnectTearsSessionDown is the whole point of listening: a server
// that decides a subscriber should go offline must be able to make that
// happen, and the accounting must say who ended it.
func TestDisconnectTearsSessionDown(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})
	cfg, addr := coaConfig(t, srv)

	done := make(chan *Result, 1)
	go func() {
		res, err := Run(context.Background(), cfg)
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()

	acctID := waitForStart(t, srv)

	reply := sendDynAuth(t, addr, radius.CodeDisconnectRequest, testSecret,
		radius.Attribute{Type: radius.AttrAcctSessionID, Value: []byte(acctID)})
	if reply == nil {
		t.Fatal("no reply to the Disconnect-Request")
	}
	if reply.Code != radius.CodeDisconnectACK {
		t.Errorf("reply code = %v, want Disconnect-ACK", reply.Code)
	}

	var res *Result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not finish after the disconnect")
	}

	// The session was held for five seconds but disconnected almost at once.
	if res.Elapsed > 4*time.Second {
		t.Errorf("run took %s, want the disconnect to have ended it early", res.Elapsed)
	}
	if res.DynAuth.DisconnectRequests != 1 || res.DynAuth.DisconnectACKs != 1 {
		t.Errorf("dynauth counters = %d requests, %d acks; want 1 and 1",
			res.DynAuth.DisconnectRequests, res.DynAuth.DisconnectACKs)
	}

	stops := srv.seen(acctStop)
	if len(stops) != 1 {
		t.Fatalf("saw %d Accounting-Stop requests, want 1", len(stops))
	}
	cause, ok := stops[0].GetUint32(radius.AttrAcctTerminateCause)
	if !ok {
		t.Fatal("Accounting-Stop carried no Acct-Terminate-Cause")
	}
	if cause != causeAdminReset {
		t.Errorf("Acct-Terminate-Cause = %d, want %d (Admin-Reset); the record must say the "+
			"server ended the session, not the subscriber", cause, causeAdminReset)
	}
}

// TestDisconnectForUnknownSessionIsNAKed tells the server its request did not
// land, which for a mass disconnect is the number that matters.
func TestDisconnectForUnknownSessionIsNAKed(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})
	cfg, addr := coaConfig(t, srv)

	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), cfg)
		done <- res
	}()
	waitForStart(t, srv)

	reply := sendDynAuth(t, addr, radius.CodeDisconnectRequest, testSecret,
		radius.Attribute{Type: radius.AttrAcctSessionID, Value: []byte("NO-SUCH-SESSION")})
	if reply == nil {
		t.Fatal("no reply to the Disconnect-Request")
	}
	if reply.Code != radius.CodeDisconnectNAK {
		t.Errorf("reply code = %v, want Disconnect-NAK", reply.Code)
	}
	cause, ok := reply.GetUint32(radius.AttrErrorCause)
	if !ok {
		t.Error("the NAK carried no Error-Cause")
	} else if cause != errSessionNotFound {
		t.Errorf("Error-Cause = %d, want %d (Session-Context-Not-Found)", cause, errSessionNotFound)
	}

	res := <-done
	if res.DynAuth.DisconnectNAKs != 1 {
		t.Errorf("DisconnectNAKs = %d, want 1", res.DynAuth.DisconnectNAKs)
	}
}

// TestDisconnectWithWrongSecretIsIgnored is a security property. An
// unauthenticated Disconnect-Request is exactly how an attacker would knock
// subscribers offline, and answering it at all would confirm the address is
// live.
func TestDisconnectWithWrongSecretIsIgnored(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})
	cfg, addr := coaConfig(t, srv)
	cfg.Hold = 800 * time.Millisecond

	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), cfg)
		done <- res
	}()
	acctID := waitForStart(t, srv)

	reply := sendDynAuth(t, addr, radius.CodeDisconnectRequest, []byte("the wrong secret"),
		radius.Attribute{Type: radius.AttrAcctSessionID, Value: []byte(acctID)})
	if reply != nil {
		t.Errorf("got a %v reply to an unauthenticated request, want silence", reply.Code)
	}

	res := <-done
	if res.DynAuth.Unverified != 1 {
		t.Errorf("Unverified = %d, want 1", res.DynAuth.Unverified)
	}
	if res.DynAuth.DisconnectACKs != 0 {
		t.Error("an unauthenticated disconnect was acknowledged")
	}

	// The session must have run its full hold rather than being torn down.
	stops := srv.seen(acctStop)
	if len(stops) != 1 {
		t.Fatalf("saw %d Accounting-Stop requests, want 1", len(stops))
	}
	if cause, _ := stops[0].GetUint32(radius.AttrAcctTerminateCause); cause == causeAdminReset {
		t.Error("the session was disconnected by an unauthenticated request")
	}
}

// TestCoAChangesTheInterimInterval checks that a policy change actually takes
// effect rather than being acknowledged and ignored.
func TestCoAChangesTheInterimInterval(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})
	cfg, addr := coaConfig(t, srv)
	cfg.Hold = 1200 * time.Millisecond
	// Start with an interval longer than the hold, so without the change no
	// interim update would ever be sent.
	cfg.InterimInterval = 10 * time.Second

	done := make(chan *Result, 1)
	go func() {
		res, err := Run(context.Background(), cfg)
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()
	acctID := waitForStart(t, srv)

	var interval [4]byte
	binary.BigEndian.PutUint32(interval[:], 1) // one second

	reply := sendDynAuth(t, addr, radius.CodeCoARequest, testSecret,
		radius.Attribute{Type: radius.AttrAcctSessionID, Value: []byte(acctID)},
		radius.Attribute{Type: radius.AttrAcctInterimInterval, Value: interval[:]})
	if reply == nil {
		t.Fatal("no reply to the CoA-Request")
	}
	if reply.Code != radius.CodeCoAACK {
		t.Errorf("reply code = %v, want CoA-ACK", reply.Code)
	}

	res := <-done
	if res.DynAuth.CoARequests != 1 || res.DynAuth.CoAACKs != 1 {
		t.Errorf("dynauth counters = %d requests, %d acks; want 1 and 1",
			res.DynAuth.CoARequests, res.DynAuth.CoAACKs)
	}
	if got := res.Exchanges[ExchangeAcctInterim].Sent; got == 0 {
		t.Error("no Interim-Update was sent; the change of interval was acknowledged but not applied")
	}
}

// TestCoAWithoutASessionIdentifierIsNAKed keeps a request we cannot act on
// from being acknowledged as though we had.
func TestCoAWithoutASessionIdentifierIsNAKed(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})
	cfg, addr := coaConfig(t, srv)
	cfg.Hold = 800 * time.Millisecond

	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), cfg)
		done <- res
	}()
	waitForStart(t, srv)

	reply := sendDynAuth(t, addr, radius.CodeCoARequest, testSecret,
		radius.Attribute{Type: radius.AttrUserName, Value: []byte("someone@example.net")})
	if reply == nil {
		t.Fatal("no reply to the CoA-Request")
	}
	if reply.Code != radius.CodeCoANAK {
		t.Errorf("reply code = %v, want CoA-NAK", reply.Code)
	}

	<-done
}
