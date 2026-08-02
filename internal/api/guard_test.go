package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCrossOriginRunIsRefused is the regression test for the way a page on any
// site the operator visits could start a run against any address it liked. A
// form post is the shape that matters: it needs no JavaScript, and because
// text/plain is a content type a form may set, the browser sends it without
// asking permission first.
func TestCrossOriginRunIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)
	addr := echoServer(t)
	body := `{"server":"` + addr + `","secret":"testing123","count":1}`

	// Two independent layers refuse these, and which one answers depends on
	// the order they run in: a foreign Origin is refused before the content
	// type is looked at. Both are asserted, so that removing either is a test
	// failure rather than a silent loss of defence.
	tests := []struct {
		name        string
		contentType string
		origin      string
		wantCode    int
	}{
		// The origin layer, for anything announcing where it came from.
		{"form post as text/plain", "text/plain;charset=UTF-8", "https://evil.example.com", http.StatusForbidden},
		{"form post as urlencoded", "application/x-www-form-urlencoded", "https://evil.example.com", http.StatusForbidden},
		{"json from another origin", "application/json", "https://evil.example.com", http.StatusForbidden},
		{"opaque origin", "application/json", "null", http.StatusForbidden},

		// The content-type layer, which is what forces a browser to ask
		// permission before it ever sends the request.
		{"form post with no origin", "text/plain;charset=UTF-8", "", http.StatusUnsupportedMediaType},
		{"urlencoded with no origin", "application/x-www-form-urlencoded", "", http.StatusUnsupportedMediaType},
		{"multipart with no origin", "multipart/form-data; boundary=x", "", http.StatusUnsupportedMediaType},
		{"no content type at all", "", "", http.StatusUnsupportedMediaType},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/run", strings.NewReader(body))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer func() { _ = res.Body.Close() }()

			if res.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantCode)
			}
			if st := getStatus(t, ts).State; st == StateRunning {
				t.Fatal("the run started anyway")
			}
		})
	}
}

// A request from the interface itself must still work, or the fix has broken
// the thing it was protecting.
func TestSameOriginRunIsAllowed(t *testing.T) {
	ts, _ := newTestServer(t)
	addr := echoServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/run",
		strings.NewReader(`{"server":"`+addr+`","secret":"testing123","count":1}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)

	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}
}

// TestRebindingHostIsRefused covers the other half: an attacker who owns a name
// can point it at loopback after their page has loaded, and the browser will
// then call their page same-origin. Refusing a Host that is a name rather than
// an address is what stops that, because there is nothing to rebind.
func TestRebindingHostIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Host = "evil.example.com"

	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}
}

// An operator who has put the interface behind something addressing it by name
// can say so, and then it works.
func TestAllowedHostIsAccepted(t *testing.T) {
	srv := New(nil, nil, nil)
	srv.AllowHost("radblaster.internal")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Host = "radblaster.internal"

	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

func TestCheckHost(t *testing.T) {
	allowed := map[string]bool{"radblaster.internal": true}

	tests := []struct {
		host string
		ok   bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"192.168.1.5:8080", true},
		{"127.0.0.1", true},
		{"radblaster.internal", true},
		{"radblaster.internal:8080", true},
		{"evil.example.com:8080", false},
		{"attacker.test", false},
		{"", false},
	}

	for _, tc := range tests {
		err := checkHost(tc.host, allowed)
		if (err == nil) != tc.ok {
			t.Errorf("checkHost(%q) error = %v, want ok = %v", tc.host, err, tc.ok)
		}
	}
}
