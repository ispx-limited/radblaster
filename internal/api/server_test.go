package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// echoServer answers every request, so a test can drive a real run through the
// interface rather than mocking the engine out from under it.
func echoServer(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, radius.MaxPacketLen)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < radius.HeaderLen {
				continue
			}
			code := radius.CodeAccessAccept
			if radius.Code(buf[0]) == radius.CodeAccountingRequest {
				code = radius.CodeAccountingResponse
			}
			out := []byte{byte(code), buf[1], 0, 0}
			out = append(out, buf[4:radius.HeaderLen]...)
			binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
			h := md5.New()
			h.Write(out)
			h.Write([]byte("testing123"))
			copy(out[4:radius.HeaderLen], h.Sum(nil))
			_, _ = conn.WriteToUDP(out, from)
		}
	}()
	return conn.LocalAddr().String()
}

func newTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	srv := New(nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return ts, srv
}

// post sends a run request and returns the status code with the decoded body.
// The body is read and closed here so callers cannot leak it.
func post(t *testing.T, ts *httptest.Server, body string) (int, map[string]any) {
	t.Helper()
	res, err := ts.Client().Post(ts.URL+"/api/run", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return res.StatusCode, out
}

func getStatus(t *testing.T, ts *httptest.Server) Status {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	var st Status
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatalf("decoding status: %v", err)
	}
	return st
}

func TestStatusStartsIdle(t *testing.T) {
	ts, _ := newTestServer(t)

	if got := getStatus(t, ts).State; got != StateIdle {
		t.Errorf("state = %q, want idle", got)
	}
}

func TestRunLifecycle(t *testing.T) {
	ts, _ := newTestServer(t)
	addr := echoServer(t)

	code, _ := post(t, ts, `{"server":"`+addr+`","secret":"testing123","count":20,"timeout":"1s"}`)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}

	deadline := time.Now().Add(10 * time.Second)
	var st Status
	for time.Now().Before(deadline) {
		st = getStatus(t, ts)
		if st.State == StateDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.State != StateDone {
		t.Fatalf("state = %q, want done", st.State)
	}
	if len(st.Exchanges) == 0 {
		t.Fatal("no exchanges were reported")
	}
	if st.Exchanges[0].Sent != 20 || st.Exchanges[0].OK != 20 {
		t.Errorf("Access-Request = %d sent, %d ok; want 20 and 20",
			st.Exchanges[0].Sent, st.Exchanges[0].OK)
	}
}

// TestSecretIsNeverReturned matters because the interface has no
// authentication of its own: anything it hands back is readable by whoever can
// reach it.
func TestSecretIsNeverReturned(t *testing.T) {
	ts, _ := newTestServer(t)
	addr := echoServer(t)

	body := `{"server":"` + addr + `","secret":"hunter2","password":"s3cret","count":2,"timeout":"1s"}`
	code, out := post(t, ts, body)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}

	raw, _ := json.Marshal(out)
	for _, leak := range []string{"hunter2", "s3cret"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("the response contains %q", leak)
		}
	}

	st := getStatus(t, ts)
	rawStatus, _ := json.Marshal(st)
	for _, leak := range []string{"hunter2", "s3cret"} {
		if bytes.Contains(rawStatus, []byte(leak)) {
			t.Errorf("the status contains %q", leak)
		}
	}
}

func TestRunRejectsBadSettings(t *testing.T) {
	ts, _ := newTestServer(t)

	tests := []struct {
		name string
		body string
	}{
		{"no server", `{"secret":"x","count":1}`},
		{"no secret", `{"server":"127.0.0.1:1812","count":1}`},
		{"no bound", `{"server":"127.0.0.1:1812","secret":"x"}`},
		{"bad duration", `{"server":"127.0.0.1:1812","secret":"x","duration":"soon"}`},
		{"bad auth", `{"server":"127.0.0.1:1812","secret":"x","count":1,"auth":"eap"}`},
		{"bad attribute", `{"server":"127.0.0.1:1812","secret":"x","count":1,"attributes":["Nope=1"]}`},
		{"bad nas ip", `{"server":"127.0.0.1:1812","secret":"x","count":1,"nasIPAddress":"not-an-ip"}`},
		{"malformed json", `{`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out := post(t, ts, tc.body)
			if code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", code)
			}
			if out["error"] == nil || out["error"] == "" {
				t.Error("the refusal did not say why")
			}
		})
	}
}

func TestOnlyOneRunAtATime(t *testing.T) {
	ts, _ := newTestServer(t)
	addr := echoServer(t)

	body := `{"server":"` + addr + `","secret":"testing123","duration":"5s","rate":10,"timeout":"1s"}`
	if code, _ := post(t, ts, body); code != http.StatusAccepted {
		t.Fatalf("first run: status = %d, want 202", code)
	}

	code, out := post(t, ts, body)
	if code != http.StatusConflict {
		t.Errorf("second run: status = %d, want 409", code)
	}
	if out["error"] == nil {
		t.Error("the refusal did not say why")
	}
}

func TestStopEndsTheRun(t *testing.T) {
	ts, _ := newTestServer(t)
	addr := echoServer(t)

	body := `{"server":"` + addr + `","secret":"testing123","duration":"5m","rate":50,"timeout":"1s"}`
	if code, _ := post(t, ts, body); code != http.StatusAccepted {
		t.Fatal("the run did not start")
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/run", nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if getStatus(t, ts).State == StateDone {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the run did not stop when asked")
}

func TestStopWithNoRunIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/run", nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
}

// TestStreamSendsStateOnConnect is what lets a browser arriving partway
// through a run, or reconnecting after a drop, see the whole picture rather
// than only what changed afterwards.
func TestStreamSendsStateOnConnect(t *testing.T) {
	ts, _ := newTestServer(t)
	addr := echoServer(t)

	body := `{"server":"` + addr + `","secret":"testing123","duration":"5s","rate":20,"timeout":"1s"}`
	if code, _ := post(t, ts, body); code != http.StatusAccepted {
		t.Fatal("the run did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type = %q, want text/event-stream", ct)
	}

	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var st Status
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &st); err != nil {
			t.Fatalf("decoding an event: %v", err)
		}
		if st.State != StateRunning {
			t.Errorf("first event state = %q, want running", st.State)
		}
		return
	}
	t.Error("the stream sent nothing")
}

func TestUIIsServedWhenPresent(t *testing.T) {
	srv := New(nil, testUI{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.Close()

	res, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}
