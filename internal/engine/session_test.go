package engine

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// acctServer answers both authentication and accounting, and keeps every
// request it saw so tests can assert on what actually went on the wire rather
// than on what the engine believes it sent.
type acctServer struct {
	authConn *net.UDPConn
	acctConn *net.UDPConn

	// acceptAttrs are added to every Access-Accept.
	acceptAttrs []radius.Attribute

	mu       sync.Mutex
	requests []*radius.Packet

	wg sync.WaitGroup
}

func newAcctServer(t *testing.T, s *acctServer) *acctServer {
	t.Helper()

	var err error
	if s.authConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}); err != nil {
		t.Fatalf("listen auth: %v", err)
	}
	if s.acctConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}); err != nil {
		t.Fatalf("listen acct: %v", err)
	}

	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.serve(s.authConn, radius.CodeAccessAccept) }()
	go func() { defer s.wg.Done(); s.serve(s.acctConn, radius.CodeAccountingResponse) }()

	t.Cleanup(func() {
		_ = s.authConn.Close()
		_ = s.acctConn.Close()
		s.wg.Wait()
	})
	return s
}

func (s *acctServer) authAddr() string { return s.authConn.LocalAddr().String() }
func (s *acctServer) acctAddr() string { return s.acctConn.LocalAddr().String() }

func (s *acctServer) serve(conn *net.UDPConn, code radius.Code) {
	buf := make([]byte, radius.MaxPacketLen)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		req := make([]byte, n)
		copy(req, buf[:n])

		if decoded, err := radius.Decode(req); err == nil {
			// Copy the values out; the decoded packet aliases req.
			for i := range decoded.Attributes {
				v := decoded.Attributes[i].Value
				decoded.Attributes[i].Value = append([]byte(nil), v...)
			}
			s.mu.Lock()
			s.requests = append(s.requests, decoded)
			s.mu.Unlock()
		}

		var reqAuth [radius.AuthenticatorLen]byte
		copy(reqAuth[:], req[4:radius.HeaderLen])

		out := []byte{byte(code), req[1], 0, 0}
		out = append(out, reqAuth[:]...)
		if code == radius.CodeAccessAccept {
			for _, a := range s.acceptAttrs {
				out = append(out, byte(a.Type), byte(len(a.Value)+2))
				out = append(out, a.Value...)
			}
		}
		binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))

		h := md5.New()
		h.Write(out)
		h.Write(testSecret)
		copy(out[4:radius.HeaderLen], h.Sum(nil))

		_, _ = conn.WriteToUDP(out, from)
	}
}

// seen returns every request of the given accounting status type.
func (s *acctServer) seen(status uint32) []*radius.Packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*radius.Packet
	for _, p := range s.requests {
		if p.Code != radius.CodeAccountingRequest {
			continue
		}
		if v, ok := p.GetUint32(radius.AttrAcctStatusType); ok && v == status {
			out = append(out, p)
		}
	}
	return out
}

func sessionConfig(s *acctServer) Config {
	return Config{
		Server:          s.authAddr(),
		AcctServer:      s.acctAddr(),
		Secret:          testSecret,
		Username:        "subscriber{n}@example.net",
		Password:        "DingDong",
		Auth:            AuthPAP,
		Links:           1,
		Timeout:         2 * time.Second,
		Seed:            1,
		InterimInterval: 50 * time.Millisecond,
	}
}

// TestSessionLifecycle is the whole point of the session model: one subscriber
// must produce an authentication, a start, updates while online, and a stop.
func TestSessionLifecycle(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 220 * time.Millisecond

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := res.Access().Accepted; got != 1 {
		t.Fatalf("Access-Request accepted = %d, want 1", got)
	}
	if got := res.Exchanges[ExchangeAcctStart].Sent; got != 1 {
		t.Errorf("Accounting-Start sent = %d, want 1", got)
	}
	if got := res.Exchanges[ExchangeAcctStop].Sent; got != 1 {
		t.Errorf("Accounting-Stop sent = %d, want 1", got)
	}
	// Roughly four 50ms updates across a 220ms hold; allow slack for timers.
	if got := res.Exchanges[ExchangeAcctInterim].Sent; got < 2 || got > 6 {
		t.Errorf("Interim-Update sent = %d, want roughly 4", got)
	}
	if res.SessionsStarted != 1 || res.SessionsCompleted != 1 {
		t.Errorf("sessions started/completed = %d/%d, want 1/1",
			res.SessionsStarted, res.SessionsCompleted)
	}
}

// TestAcctSessionIDIsStableAcrossTheLifecycle matters because a server keys
// its session table on it: a value that changed between Start and Stop would
// leave a session open forever.
func TestAcctSessionIDIsStableAcrossTheLifecycle(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 150 * time.Millisecond

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	starts := srv.seen(acctStart)
	stops := srv.seen(acctStop)
	if len(starts) != 1 || len(stops) != 1 {
		t.Fatalf("saw %d starts and %d stops, want 1 and 1", len(starts), len(stops))
	}

	startID := string(starts[0].Get(radius.AttrAcctSessionID))
	stopID := string(stops[0].Get(radius.AttrAcctSessionID))
	if startID == "" {
		t.Fatal("Accounting-Start carried no Acct-Session-Id")
	}
	if startID != stopID {
		t.Errorf("Acct-Session-Id changed during the session: start %q, stop %q", startID, stopID)
	}
	for _, p := range srv.seen(acctInterim) {
		if got := string(p.Get(radius.AttrAcctSessionID)); got != startID {
			t.Errorf("interim update carried Acct-Session-Id %q, want %q", got, startID)
		}
	}
}

// TestClassIsEchoedIntoAccounting checks a requirement of RFC 2865 that also
// exercises the whole chain, including any proxy between us and the server.
func TestClassIsEchoedIntoAccounting(t *testing.T) {
	class := []byte{0xde, 0xad, 0xbe, 0xef}
	srv := newAcctServer(t, &acctServer{
		acceptAttrs: []radius.Attribute{{Type: radius.AttrClass, Value: class}},
	})

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 120 * time.Millisecond

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, status := range []uint32{acctStart, acctInterim, acctStop} {
		pkts := srv.seen(status)
		if len(pkts) == 0 {
			t.Errorf("no accounting request of status %d was sent", status)
			continue
		}
		for _, p := range pkts {
			got := p.Get(radius.AttrClass)
			if string(got) != string(class) {
				t.Errorf("status %d carried Class %x, want %x", status, got, class)
			}
		}
	}
}

// TestFramedIPIsEchoedIntoAccounting keeps the address the server assigned
// attached to the session it belongs to.
func TestFramedIPIsEchoedIntoAccounting(t *testing.T) {
	ip := []byte{192, 0, 2, 77}
	srv := newAcctServer(t, &acctServer{
		acceptAttrs: []radius.Attribute{{Type: radius.AttrFramedIPAddress, Value: ip}},
	})

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 80 * time.Millisecond

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	starts := srv.seen(acctStart)
	if len(starts) != 1 {
		t.Fatalf("saw %d Accounting-Start requests, want 1", len(starts))
	}
	if got := starts[0].Get(radius.AttrFramedIPAddress); string(got) != string(ip) {
		t.Errorf("Framed-IP-Address = %v, want %v", got, ip)
	}
}

// TestServerInterimIntervalIsHonoured matters because the server is telling us
// how much accounting load it wants. Ignoring it would mean testing a cadence
// the operator never asked for.
func TestServerInterimIntervalIsHonoured(t *testing.T) {
	// One second, far slower than the configured 50ms fallback, so a session
	// held for 300ms should produce no interim update at all.
	var interval [4]byte
	binary.BigEndian.PutUint32(interval[:], 1)

	srv := newAcctServer(t, &acctServer{
		acceptAttrs: []radius.Attribute{{Type: radius.AttrAcctInterimInterval, Value: interval[:]}},
	})

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 300 * time.Millisecond

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := res.Exchanges[ExchangeAcctInterim].Sent; got != 0 {
		t.Errorf("Interim-Update sent = %d, want 0; the server asked for one every second "+
			"and the session was only online for 300ms", got)
	}
	if got := res.Exchanges[ExchangeAcctStop].Sent; got != 1 {
		t.Errorf("Accounting-Stop sent = %d, want 1", got)
	}
}

// TestGigawordsCarryTheOverflow reproduces the 32-bit octet counter rollover,
// which plenty of accounting pipelines mishandle and which a load tool that
// quietly stays under four gibibytes would never surface.
func TestGigawordsCarryTheOverflow(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 150 * time.Millisecond
	cfg.InterimInterval = 40 * time.Millisecond
	// Ten gibibytes per second, so the counter passes the 32-bit boundary
	// almost immediately.
	cfg.TrafficUp = 10 << 30
	cfg.TrafficDown = 10 << 30

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	stops := srv.seen(acctStop)
	if len(stops) != 1 {
		t.Fatalf("saw %d Accounting-Stop requests, want 1", len(stops))
	}
	stop := stops[0]

	gw, ok := stop.GetUint32(radius.AttrAcctInputGigawords)
	if !ok {
		t.Fatal("Acct-Input-Gigawords was absent despite the counter exceeding 32 bits")
	}
	if gw == 0 {
		t.Error("Acct-Input-Gigawords = 0, want the overflow to be carried")
	}
	if _, ok := stop.GetUint32(radius.AttrAcctInputOctets); !ok {
		t.Error("Acct-Input-Octets was absent")
	}

	// The low word and the high word together must reconstruct a value above
	// the 32-bit boundary.
	low, _ := stop.GetUint32(radius.AttrAcctInputOctets)
	total := uint64(gw)<<32 | uint64(low)
	if total <= 1<<32 {
		t.Errorf("reconstructed input octets = %d, want more than %d", total, uint64(1)<<32)
	}
}

// TestStopCarriesSessionTimeAndCause checks the attributes a billing system
// needs to close a session out.
func TestStopCarriesSessionTimeAndCause(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 120 * time.Millisecond

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	stops := srv.seen(acctStop)
	if len(stops) != 1 {
		t.Fatalf("saw %d Accounting-Stop requests, want 1", len(stops))
	}
	if _, ok := stops[0].GetUint32(radius.AttrAcctSessionTime); !ok {
		t.Error("Accounting-Stop carried no Acct-Session-Time")
	}
	cause, ok := stops[0].GetUint32(radius.AttrAcctTerminateCause)
	if !ok {
		t.Error("Accounting-Stop carried no Acct-Terminate-Cause")
	} else if cause != 1 {
		t.Errorf("Acct-Terminate-Cause = %d, want 1 (User-Request)", cause)
	}

	// A start must not carry usage counters; there has been no usage yet.
	starts := srv.seen(acctStart)
	if len(starts) == 1 {
		if _, ok := starts[0].GetUint32(radius.AttrAcctSessionTime); ok {
			t.Error("Accounting-Start carried Acct-Session-Time, which belongs on updates and stops")
		}
	}
}

// TestNoAccountingWithoutAnAcctServer keeps the authentication-only mode
// working, which is the right shape for a pure authentication stress test.
func TestNoAccountingWithoutAnAcctServer(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	cfg := sessionConfig(srv)
	cfg.AcctServer = ""
	cfg.Count = 10
	cfg.Hold = time.Second

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Access().Accepted != 10 {
		t.Errorf("accepted = %d, want 10", res.Access().Accepted)
	}
	if res.Accounting() {
		t.Error("accounting exchanges were sent despite no accounting server being configured")
	}
	if res.SessionsCompleted != 10 {
		t.Errorf("sessions completed = %d, want 10", res.SessionsCompleted)
	}
}

// TestRejectedSessionSendsNoAccounting is a correctness property: a subscriber
// the server refused must not appear in accounting as though it came online.
func TestRejectedSessionSendsNoAccounting(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})
	// Replace the auth responder with one that rejects.
	_ = srv.authConn.Close()

	rejector := newTestServer(t, &testServer{code: radius.CodeAccessReject})

	cfg := sessionConfig(srv)
	cfg.Server = rejector.addr()
	cfg.Count = 5
	cfg.Hold = 100 * time.Millisecond

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Access().Rejected != 5 {
		t.Errorf("rejected = %d, want 5", res.Access().Rejected)
	}
	if res.Accounting() {
		t.Error("a rejected subscriber produced accounting records")
	}
	if res.SessionsStarted != 0 {
		t.Errorf("sessions started = %d, want 0; none of them authenticated", res.SessionsStarted)
	}
}

func TestManySessionsCompleteTheirLifecycle(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	cfg := sessionConfig(srv)
	cfg.Links = 4
	cfg.Count = 200
	cfg.Hold = 60 * time.Millisecond
	cfg.InterimInterval = 30 * time.Millisecond

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.SessionsStarted != 200 {
		t.Errorf("sessions started = %d, want 200", res.SessionsStarted)
	}
	if res.SessionsCompleted != 200 {
		t.Errorf("sessions completed = %d, want 200", res.SessionsCompleted)
	}
	if got := res.Exchanges[ExchangeAcctStart].Sent; got != 200 {
		t.Errorf("Accounting-Start sent = %d, want 200", got)
	}
	if got := res.Exchanges[ExchangeAcctStop].Sent; got != 200 {
		t.Errorf("Accounting-Stop sent = %d, want 200", got)
	}
}

// TestExtraAttributesReachTheWire covers the path vendor attributes take: a
// caller names them, they are resolved through a dictionary, and they appear
// on every request the session sends.
func TestExtraAttributesReachTheWire(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	dict := radius.StandardDictionary()
	specs := make([]radius.AttrSpec, 0, 3)
	for _, s := range []string{
		"9.1=ip:qos-policy-in=voice", // a vendor attribute by number
		"3561.1=eth 1/1/{n}",         // templated per subscriber
		"NAS-Port-Id=slot-{n}",       // a standard attribute, templated
	} {
		spec, err := dict.ParseSpec(s)
		if err != nil {
			t.Fatalf("ParseSpec(%q): %v", s, err)
		}
		specs = append(specs, spec)
	}

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Hold = 80 * time.Millisecond
	cfg.Attributes = specs

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	starts := srv.seen(acctStart)
	if len(starts) != 1 {
		t.Fatalf("saw %d Accounting-Start requests, want 1", len(starts))
	}
	p := starts[0]

	if got := string(p.GetVendor(9, 1)); got != "ip:qos-policy-in=voice" {
		t.Errorf("vendor 9 attribute 1 = %q, want the AV pair", got)
	}
	if got := string(p.GetVendor(3561, 1)); got != "eth 1/1/1" {
		t.Errorf("vendor 3561 attribute 1 = %q, want the template rendered for session 1", got)
	}
	if got := string(p.Get(radius.AttrNASPortID)); got != "slot-1" {
		t.Errorf("NAS-Port-Id = %q, want slot-1", got)
	}
}

// TestBadExtraAttributeFailsAtStartup keeps a value that cannot be encoded
// from being silently dropped from every packet in the run.
func TestBadExtraAttributeFailsAtStartup(t *testing.T) {
	srv := newAcctServer(t, &acctServer{})

	spec, err := radius.StandardDictionary().ParseSpec("Session-Timeout=not-a-number")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	cfg := sessionConfig(srv)
	cfg.Count = 1
	cfg.Attributes = []radius.AttrSpec{spec}

	if _, err := Run(context.Background(), cfg); err == nil {
		t.Error("expected the run to be refused, not to drop the attribute silently")
	}
}
