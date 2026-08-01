package engine

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

var testSecret = []byte("testing123")

// testServer is a controllable RADIUS server. Everything the engine is
// expected to distinguish (an accept, a reject, silence, a mis-signed reply)
// is a knob here, so each outcome can be driven deterministically.
type testServer struct {
	conn *net.UDPConn

	// replySecret signs replies. Setting it to anything else simulates a
	// shared secret mismatch.
	replySecret []byte
	// code is the reply code to send.
	code radius.Code
	// silent drops every request.
	silent atomic.Bool
	// delay is applied before replying.
	delay time.Duration

	received atomic.Uint64
	wg       sync.WaitGroup
}

func newTestServer(t *testing.T, s *testServer) *testServer {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.conn = conn
	if s.replySecret == nil {
		s.replySecret = testSecret
	}
	if s.code == 0 {
		s.code = radius.CodeAccessAccept
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.serve()
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		s.wg.Wait()
	})
	return s
}

func (s *testServer) addr() string { return s.conn.LocalAddr().String() }

func (s *testServer) serve() {
	buf := make([]byte, radius.MaxPacketLen)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		s.received.Add(1)
		if s.silent.Load() || n < radius.HeaderLen {
			continue
		}

		req := make([]byte, n)
		copy(req, buf[:n])

		reply := func() {
			var reqAuth [radius.AuthenticatorLen]byte
			copy(reqAuth[:], req[4:radius.HeaderLen])

			out := []byte{byte(s.code), req[1], 0, 0}
			out = append(out, reqAuth[:]...)
			out = append(out, byte(radius.AttrFramedIPAddress), 6, 192, 0, 2, 10)
			binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))

			h := md5.New()
			h.Write(out)
			h.Write(s.replySecret)
			copy(out[4:radius.HeaderLen], h.Sum(nil))

			_, _ = s.conn.WriteToUDP(out, from)
		}

		if s.delay > 0 {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				time.Sleep(s.delay)
				reply()
			}()
			continue
		}
		reply()
	}
}

func baseConfig(addr string) Config {
	return Config{
		Server:   addr,
		Secret:   testSecret,
		Username: "subscriber{n}@example.net",
		Password: "DingDong",
		Auth:     AuthPAP,
		Sockets:  2,
		Timeout:  2 * time.Second,
		Seed:     1,
	}
}

func TestRunAcceptsEveryRequest(t *testing.T) {
	srv := newTestServer(t, &testServer{})

	cfg := baseConfig(srv.addr())
	cfg.Count = 200

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Access.Sent != 200 {
		t.Errorf("Sent = %d, want 200", res.Access.Sent)
	}
	if res.Access.Accepted != 200 {
		t.Errorf("Accepted = %d, want 200 (timeouts %d, unverified %d, unmatched %d)",
			res.Access.Accepted, res.Access.Timeouts, res.Access.Unverified, res.Access.Unmatched)
	}
	if res.Access.Latency.Count() != 200 {
		t.Errorf("recorded %d latencies, want 200", res.Access.Latency.Count())
	}
}

func TestRunCountsRejectsSeparatelyFromFailures(t *testing.T) {
	srv := newTestServer(t, &testServer{code: radius.CodeAccessReject})

	cfg := baseConfig(srv.addr())
	cfg.Count = 50

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Access.Rejected != 50 {
		t.Errorf("Rejected = %d, want 50", res.Access.Rejected)
	}
	if res.Access.Accepted != 0 {
		t.Errorf("Accepted = %d, want 0", res.Access.Accepted)
	}
	// A rejection is a healthy server saying no, not a transport failure.
	if res.Access.Timeouts != 0 || res.Access.Unverified != 0 {
		t.Errorf("a policy rejection was recorded as a failure: timeouts %d, unverified %d",
			res.Access.Timeouts, res.Access.Unverified)
	}
}

func TestRunCountsTimeouts(t *testing.T) {
	srv := newTestServer(t, &testServer{})
	srv.silent.Store(true)

	cfg := baseConfig(srv.addr())
	cfg.Count = 20
	cfg.Timeout = 200 * time.Millisecond

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Access.Sent != 20 {
		t.Errorf("Sent = %d, want 20", res.Access.Sent)
	}
	if res.Access.Timeouts != 20 {
		t.Errorf("Timeouts = %d, want 20", res.Access.Timeouts)
	}
	if res.Access.Answered() != 0 {
		t.Errorf("Answered = %d, want 0", res.Access.Answered())
	}
}

// TestRunReportsSecretMismatchDistinctly is the diagnosis that matters most in
// practice: a wrong secret must not look like an unreachable server.
func TestRunReportsSecretMismatchDistinctly(t *testing.T) {
	srv := newTestServer(t, &testServer{replySecret: []byte("a different secret")})

	cfg := baseConfig(srv.addr())
	cfg.Count = 20
	cfg.Timeout = 300 * time.Millisecond

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Access.Unverified == 0 {
		t.Error("Unverified = 0, want the mis-signed replies to be counted")
	}
	if res.Access.Accepted != 0 {
		t.Errorf("Accepted = %d, want 0; a reply that fails verification must never count as success",
			res.Access.Accepted)
	}
}

func TestRunHonoursRate(t *testing.T) {
	srv := newTestServer(t, &testServer{})

	cfg := baseConfig(srv.addr())
	cfg.Rate = 500
	cfg.Duration = 600 * time.Millisecond
	cfg.Sockets = 2

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Allow generous slack: this asserts pacing happens at all, not that the
	// scheduler is precise, so it stays reliable on a loaded machine.
	if res.Access.Sent < 150 || res.Access.Sent > 500 {
		t.Errorf("Sent = %d over 600ms at 500/s, want roughly 300", res.Access.Sent)
	}
	if res.AchievedRate <= 0 {
		t.Error("AchievedRate was not computed")
	}
}

func TestRunStopsAtDuration(t *testing.T) {
	srv := newTestServer(t, &testServer{})

	cfg := baseConfig(srv.addr())
	cfg.Rate = 200
	cfg.Duration = 300 * time.Millisecond
	cfg.Timeout = 200 * time.Millisecond

	started := time.Now()
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The run itself plus the drain window; well short of running forever.
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("run took %s, want it bounded by the duration plus the drain", elapsed)
	}
}

func TestRunHonoursContextCancellation(t *testing.T) {
	srv := newTestServer(t, &testServer{})
	srv.silent.Store(true)

	cfg := baseConfig(srv.addr())
	cfg.Rate = 100
	cfg.Duration = time.Hour
	cfg.Timeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("run took %s after cancellation, want it to stop promptly", elapsed)
	}
}

// TestLatencyExceedsServiceTimeWhenBehindSchedule is the coordinated-omission
// property. When the run cannot keep up, latency measured from the intended
// send time must exceed the time the server actually took, because the queueing
// delay is real and a tool that measures only from the actual send hides it.
func TestLatencyIncludesSchedulingDelay(t *testing.T) {
	srv := newTestServer(t, &testServer{delay: 40 * time.Millisecond})

	cfg := baseConfig(srv.addr())
	// One socket and a rate far beyond what 256 outstanding requests at 40ms
	// each can sustain, so the schedule slips.
	cfg.Sockets = 1
	cfg.Rate = 20000
	cfg.Duration = 500 * time.Millisecond
	cfg.Timeout = 2 * time.Second

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Access.Latency.Count() == 0 {
		t.Skip("no replies were recorded, nothing to compare")
	}

	latency := res.Access.Latency.Quantile(0.5)
	service := res.Access.ServiceTime.Quantile(0.5)
	if latency < service {
		t.Errorf("median latency %v is below median service time %v; scheduling delay was lost",
			time.Duration(latency), time.Duration(service))
	}
	if res.Access.Blocked == 0 {
		t.Log("note: identifier space was not exhausted, so the schedule may not have slipped")
	}
}

// TestBlockedCountedWhenIdentifiersExhausted keeps the run honest about being
// limited by our own concurrency rather than by the server.
func TestBlockedCountedWhenIdentifiersExhausted(t *testing.T) {
	srv := newTestServer(t, &testServer{})
	srv.silent.Store(true)

	cfg := baseConfig(srv.addr())
	cfg.Sockets = 1
	cfg.Rate = 0 // unpaced: offer as fast as possible
	cfg.Duration = 300 * time.Millisecond
	cfg.Timeout = 10 * time.Second // long enough that nothing is released

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Access.Sent > radius.IDSpace {
		t.Errorf("Sent = %d with one socket and no replies, want at most %d outstanding",
			res.Access.Sent, radius.IDSpace)
	}
	if res.Access.Blocked == 0 {
		t.Error("Blocked = 0, want the exhausted identifier space to be visible")
	}
}

func TestUsernameTemplateIsPerRequest(t *testing.T) {
	for _, tc := range []struct {
		tmpl string
		seq  uint64
		want string
	}{
		{"user{n}@example.net", 1, "user1@example.net"},
		{"user{n}@example.net", 4242, "user4242@example.net"},
		{"static@example.net", 9, "static@example.net"},
		{"{n}", 7, "7"},
	} {
		if got := expandUsername(tc.tmpl, tc.seq); got != tc.want {
			t.Errorf("expandUsername(%q, %d) = %q, want %q", tc.tmpl, tc.seq, got, tc.want)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	valid := func() Config {
		c := baseConfig("127.0.0.1:1812")
		c.Count = 1
		return c
	}

	tests := []struct {
		name   string
		mangle func(*Config)
	}{
		{"no server", func(c *Config) { c.Server = "" }},
		{"no secret", func(c *Config) { c.Secret = nil }},
		{"no username", func(c *Config) { c.Username = "" }},
		{"no bound", func(c *Config) { c.Count = 0; c.Duration = 0 }},
		{"unknown auth", func(c *Config) { c.Auth = "eap" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mangle(&c)
			if _, err := Run(context.Background(), c); err == nil {
				t.Error("expected a validation error")
			}
		})
	}
}

func TestRunUsesCHAP(t *testing.T) {
	srv := newTestServer(t, &testServer{})

	cfg := baseConfig(srv.addr())
	cfg.Auth = AuthCHAP
	cfg.Count = 25

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Access.Accepted != 25 {
		t.Errorf("Accepted = %d, want 25", res.Access.Accepted)
	}
}
