package api

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The interface has no authentication of its own, and binding to loopback does
// not give it any. Loopback keeps the network out; it does not keep a browser
// out. Any page the operator happens to visit can send requests to 127.0.0.1,
// and a name the attacker controls can be pointed at 127.0.0.1 so that the
// browser treats their page as same-origin with the interface. Both routes are
// closed here, before a handler sees the request, because the ability to start
// a run against any reachable address is worth as much to an attacker as it is
// to an operator.

// guard wraps h with the checks every request has to pass first.
func guard(h http.Handler, allowed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := checkHost(r.Host, allowed); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		if err := checkOrigin(r); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		if err := checkContentType(r); err != nil {
			writeError(w, http.StatusUnsupportedMediaType, err)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// checkHost refuses a request whose Host header names something the interface
// is not served on.
//
// This is what stops DNS rebinding. An attacker who owns a name can point it at
// 127.0.0.1 after their page has loaded, at which point the browser considers
// that page same-origin with the interface and the origin check below agrees
// with it. They cannot do that with an address literal, because there is no
// name to rebind. So a Host that is neither an address nor localhost is refused
// unless the operator has named it with -allowed-host, which is there for the
// case of a reverse proxy in front.
func checkHost(host string, allowed map[string]bool) error {
	if host == "" {
		return fmt.Errorf("a Host header is required")
	}
	if allowed[strings.ToLower(host)] {
		return nil
	}

	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.ToLower(strings.Trim(name, "[]"))

	if allowed[name] || name == "localhost" || net.ParseIP(name) != nil {
		return nil
	}
	return fmt.Errorf("refusing a request addressed to %q: the interface answers on an "+
		"address, not a name, so that a name cannot be repointed at it. Use -allowed-host "+
		"if this instance is behind something that needs one", host)
}

// checkOrigin refuses a cross-origin request.
//
// A browser attaches Origin to everything that carries a side effect, so a page
// on another site cannot reach a handler without saying where it came from. A
// request with no Origin at all did not come from a browser -- the command
// line, a script, a health check -- and none of those can be driven by a site
// the operator visits, so they are left alone.
func checkOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	if origin == "null" {
		return fmt.Errorf("refusing a request from an opaque origin")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return fmt.Errorf("refusing a request with an unreadable Origin %q", origin)
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return fmt.Errorf("refusing a request from %s: this interface is not shared across origins", origin)
	}
	return nil
}

// checkContentType requires JSON on anything carrying a body.
//
// This is the check that does the work, and it is worth being precise about
// why. A form can only post as urlencoded, multipart or text/plain, and a
// cross-origin fetch may only set those three without asking permission first.
// Insisting on application/json therefore means the browser has to send a
// preflight, which this server never answers, so the request is stopped before
// it is ever made. The origin check above would catch it anyway; this stops it
// one step earlier and costs nothing, because everything that legitimately
// talks to this API already sends JSON.
func checkContentType(r *http.Request) error {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return nil
	}

	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return fmt.Errorf("this endpoint takes application/json; the request declared no content type")
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return fmt.Errorf("could not read the content type %q", ct)
	}
	if mt != "application/json" {
		return fmt.Errorf("this endpoint takes application/json, not %s", mt)
	}
	return nil
}
