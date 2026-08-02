// Package api serves the run-control and live-metrics interface the web
// interface is built on.
//
// Everything the interface can do goes through here, so the browser has no
// privileged path the command line lacks and either can drive a run.
package api

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ispx-limited/radblaster/internal/engine"
	"github.com/ispx-limited/radblaster/internal/profile"
	"github.com/ispx-limited/radblaster/internal/radius"
)

// State is what a run is doing.
type State string

// Run states.
const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
)

// RunConfig is a run as the interface describes it.
//
// Durations are strings so a form can send "30s" rather than a nanosecond
// count, and the secret is write-only: it is accepted here but never returned,
// because a shared secret should not be readable from a browser that happens
// to be pointed at the interface.
type RunConfig struct {
	Server     string `json:"server"`
	AcctServer string `json:"acctServer,omitempty"`
	Secret     string `json:"secret,omitempty"`

	Rate     float64 `json:"rate,omitempty"`
	Count    uint64  `json:"count,omitempty"`
	Duration string  `json:"duration,omitempty"`
	Hold     string  `json:"hold,omitempty"`
	Interim  string  `json:"interim,omitempty"`

	TrafficUp      uint64 `json:"trafficUp,omitempty"`
	TrafficUpMax   uint64 `json:"trafficUpMax,omitempty"`
	TrafficDown    uint64 `json:"trafficDown,omitempty"`
	TrafficDownMax uint64 `json:"trafficDownMax,omitempty"`

	RejectRate     float64 `json:"rejectRate,omitempty"`
	RejectUsername string  `json:"rejectUsername,omitempty"`

	Shape       string  `json:"shape,omitempty"`
	BurstPeriod string  `json:"burstPeriod,omitempty"`
	BurstLow    float64 `json:"burstLow,omitempty"`

	Links   int    `json:"links,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	Retries int    `json:"retries"`

	Auth     string `json:"auth,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	NASIdentifier    string `json:"nasIdentifier,omitempty"`
	NASIPAddress     string `json:"nasIPAddress,omitempty"`
	NASPortType      uint32 `json:"nasPortType,omitempty"`
	CallingStationID string `json:"callingStationId,omitempty"`
	NASPortID        string `json:"nasPortId,omitempty"`

	Attributes   []profile.Attr `json:"attributes,omitempty"`
	Dictionaries []string       `json:"dictionaries,omitempty"`
	CoAListen    string         `json:"coaListen,omitempty"`
	// AllowMissingMessageAuthenticator accepts replies that carry no
	// Message-Authenticator. See engine.Config for why that is refused by
	// default.
	AllowMissingMessageAuthenticator bool   `json:"allowMissingMessageAuthenticator,omitempty"`
	Seed                             uint64 `json:"seed,omitempty"`
}

// redacted returns a copy safe to hand back to a client.
func (c RunConfig) redacted() RunConfig {
	c.Secret = ""
	c.Password = ""
	return c
}

// toEngine turns an interface request into an engine configuration, reporting
// anything it cannot make sense of rather than quietly substituting a default.
func (c RunConfig) toEngine(dict *radius.Dictionary) (engine.Config, error) {
	var cfg engine.Config

	if strings.TrimSpace(c.Server) == "" {
		return cfg, errors.New("a server is required")
	}
	if strings.TrimSpace(c.Secret) == "" {
		return cfg, errors.New("a shared secret is required")
	}

	cfg.Server = withDefaultPort(c.Server, "1812")
	if c.AcctServer != "" {
		cfg.AcctServer = withDefaultPort(c.AcctServer, "1813")
	}
	cfg.Secret = []byte(c.Secret)
	cfg.Rate = c.Rate
	cfg.Count = c.Count
	cfg.Links = c.Links
	cfg.Retries = c.Retries
	cfg.TrafficUp = c.TrafficUp
	cfg.TrafficUpMax = c.TrafficUpMax
	cfg.TrafficDown = c.TrafficDown
	cfg.TrafficDownMax = c.TrafficDownMax
	cfg.RejectRate = c.RejectRate
	cfg.RejectUsername = c.RejectUsername
	cfg.BurstLow = c.BurstLow
	cfg.NASIdentifier = c.NASIdentifier
	cfg.NASPortType = c.NASPortType
	cfg.CallingStationID = c.CallingStationID
	cfg.NASPortID = c.NASPortID
	cfg.CoAListen = c.CoAListen
	cfg.Seed = c.Seed
	cfg.AllowMissingMessageAuthenticator = c.AllowMissingMessageAuthenticator

	cfg.Username = c.Username
	if cfg.Username == "" {
		cfg.Username = "subscriber{n}@example.net"
	}
	cfg.Password = c.Password

	switch strings.ToLower(c.Auth) {
	case "", "pap":
		cfg.Auth = engine.AuthPAP
	case "chap":
		cfg.Auth = engine.AuthCHAP
	case "ipoe":
		cfg.Auth = engine.AuthIPoE
	default:
		return cfg, fmt.Errorf("unknown access method %q", c.Auth)
	}

	switch strings.ToLower(c.Shape) {
	case "", "steady":
		cfg.Shape = engine.ShapeSteady
	case "ramp":
		cfg.Shape = engine.ShapeRamp
	case "burst":
		cfg.Shape = engine.ShapeBurst
	case "stampede":
		cfg.Shape = engine.ShapeStampede
	default:
		return cfg, fmt.Errorf("unknown load shape %q", c.Shape)
	}

	var err error
	if cfg.Duration, err = parseDuration("duration", c.Duration); err != nil {
		return cfg, err
	}
	if cfg.Hold, err = parseDuration("hold", c.Hold); err != nil {
		return cfg, err
	}
	if cfg.InterimInterval, err = parseDuration("interim", c.Interim); err != nil {
		return cfg, err
	}
	if cfg.Timeout, err = parseDuration("timeout", c.Timeout); err != nil {
		return cfg, err
	}
	if cfg.BurstPeriod, err = parseDuration("burst period", c.BurstPeriod); err != nil {
		return cfg, err
	}

	if c.NASIPAddress != "" {
		ip := net.ParseIP(c.NASIPAddress)
		if ip == nil || ip.To4() == nil {
			return cfg, fmt.Errorf("%q is not an IPv4 address", c.NASIPAddress)
		}
		cfg.NASIPAddress = ip.To4()
	}

	for _, a := range c.Attributes {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		spec, err := dict.ParseSpec(name + "=" + a.Value)
		if err != nil {
			return cfg, err
		}
		cfg.Attributes = append(cfg.Attributes, spec)
	}

	if cfg.Count == 0 && cfg.Duration == 0 {
		return cfg, errors.New("a run needs either a session count or a duration")
	}
	return cfg, nil
}

func parseDuration(name, s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration, try something like 30s or 5m", name, s)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s cannot be negative", name)
	}
	return d, nil
}

func withDefaultPort(host, port string) string {
	if strings.Contains(host, ":") {
		return host
	}
	return host + ":" + port
}

// Exchange is one row of the results table.
//
// Latencies are nanoseconds so the browser can format them itself rather than
// having a choice of unit baked in here.
type Exchange struct {
	Name        string  `json:"name"`
	Sent        uint64  `json:"sent"`
	OK          uint64  `json:"ok"`
	Rejected    uint64  `json:"rejected"`
	Retransmits uint64  `json:"retransmits"`
	Timeouts    uint64  `json:"timeouts"`
	Unverified  uint64  `json:"unverified"`
	Blocked     uint64  `json:"blocked"`
	P50         uint64  `json:"p50"`
	P95         uint64  `json:"p95"`
	P99         uint64  `json:"p99"`
	P999        uint64  `json:"p999"`
	Max         uint64  `json:"max"`
	Mean        float64 `json:"mean"`
	// ServiceP50 is the same measurement taken from the actual send rather
	// than the scheduled one. A gap between them is the run failing to keep
	// pace, not the server being slow.
	ServiceP50 uint64 `json:"serviceP50"`
	ServiceP95 uint64 `json:"serviceP95"`
}

func exchangeOf(m *engine.Metrics) Exchange {
	return Exchange{
		Name:        m.Name,
		Sent:        m.Sent,
		OK:          m.Answered(),
		Rejected:    m.Rejected,
		Retransmits: m.Retransmits,
		Timeouts:    m.Timeouts,
		Unverified:  m.Unverified,
		Blocked:     m.Blocked,
		P50:         m.Latency.Quantile(0.5),
		P95:         m.Latency.Quantile(0.95),
		P99:         m.Latency.Quantile(0.99),
		P999:        m.Latency.Quantile(0.999),
		Max:         m.Latency.Max(),
		Mean:        m.Latency.Mean(),
		ServiceP50:  m.ServiceTime.Quantile(0.5),
		ServiceP95:  m.ServiceTime.Quantile(0.95),
	}
}

// DynAuth is the server-initiated request tally.
type DynAuth struct {
	DisconnectRequests uint64 `json:"disconnectRequests"`
	DisconnectACKs     uint64 `json:"disconnectAcks"`
	DisconnectNAKs     uint64 `json:"disconnectNaks"`
	CoARequests        uint64 `json:"coaRequests"`
	CoAACKs            uint64 `json:"coaAcks"`
	CoANAKs            uint64 `json:"coaNaks"`
	Unverified         uint64 `json:"unverified"`
	Malformed          uint64 `json:"malformed"`
}

func dynAuthOf(d engine.DynAuth) DynAuth {
	return DynAuth{
		DisconnectRequests: d.DisconnectRequests,
		DisconnectACKs:     d.DisconnectACKs,
		DisconnectNAKs:     d.DisconnectNAKs,
		CoARequests:        d.CoARequests,
		CoAACKs:            d.CoAACKs,
		CoANAKs:            d.CoANAKs,
		Unverified:         d.Unverified,
		Malformed:          d.Malformed,
	}
}

// Sample is one point on the live charts.
//
// The series is kept here rather than accumulated in the browser so that a
// reload, or a browser that joins partway through, still sees the whole run
// instead of starting from a blank chart.
type Sample struct {
	T        int64   `json:"t"`
	Offered  float64 `json:"offered"`
	Answered float64 `json:"answered"`
	Lost     float64 `json:"lost"`
	P95      uint64  `json:"p95"`
	Service  uint64  `json:"service"`
}

// Status is the whole of what the interface renders.
type Status struct {
	State     State     `json:"state"`
	Config    RunConfig `json:"config"`
	Error     string    `json:"error,omitempty"`
	StartedAt string    `json:"startedAt,omitempty"`

	ElapsedMS int64 `json:"elapsedMs"`

	Exchanges []Exchange `json:"exchanges"`
	DynAuth   DynAuth    `json:"dynAuth"`

	SessionsStarted   uint64 `json:"sessionsStarted"`
	SessionsCompleted uint64 `json:"sessionsCompleted"`
	SessionsOnline    uint64 `json:"sessionsOnline"`
	Outstanding       int    `json:"outstanding"`

	SetupRate    float64 `json:"setupRate"`
	AnswerRate   float64 `json:"answerRate"`
	LossRate     float64 `json:"lossRate"`
	OfferedRate  float64 `json:"offeredRate"`
	AchievedRate float64 `json:"achievedRate"`

	Links int `json:"links"`

	// History is the run's series so far, oldest first.
	History []Sample `json:"history,omitempty"`

	// Notes are the plain-language warnings that say why a result means less
	// than it appears to. Composing them here rather than in the browser keeps
	// one source of truth with the command line.
	Notes []string `json:"notes,omitempty"`
}

// ToProfile extracts the part of a run worth keeping, which is everything
// except the credentials.
func (c RunConfig) ToProfile(name string) *profile.Profile {
	return &profile.Profile{
		Name:       name,
		Server:     c.Server,
		AcctServer: c.AcctServer,
		Access:     c.Auth,
		Username:   c.Username,
		Load: profile.Load{
			Rate:        c.Rate,
			Count:       c.Count,
			Duration:    c.Duration,
			Shape:       c.Shape,
			BurstPeriod: c.BurstPeriod,
			BurstLow:    c.BurstLow,
			RejectRate:  c.RejectRate,
			Links:       c.Links,
			Timeout:     c.Timeout,
			Retries:     c.Retries,
		},
		Session: profile.Session{Hold: c.Hold, Interim: c.Interim},
		Traffic: profile.Traffic{
			Up:   profile.Range{Min: c.TrafficUp, Max: c.TrafficUpMax},
			Down: profile.Range{Min: c.TrafficDown, Max: c.TrafficDownMax},
		},
		NAS: profile.NAS{
			Identifier:       c.NASIdentifier,
			IPAddress:        c.NASIPAddress,
			PortType:         c.NASPortType,
			PortID:           c.NASPortID,
			CallingStationID: c.CallingStationID,
		},
		Attributes:   c.Attributes,
		Dictionaries: c.Dictionaries,
		CoAListen:    c.CoAListen,
		Seed:         c.Seed,
	}
}

// FromProfile fills in a run from a profile. The credentials stay empty: a
// profile never carries them, so they have to come from wherever the run is
// being started.
func FromProfile(p *profile.Profile) RunConfig {
	return RunConfig{
		Server:           p.Server,
		AcctServer:       p.AcctServer,
		Auth:             p.Access,
		Username:         p.Username,
		Rate:             p.Load.Rate,
		Count:            p.Load.Count,
		Duration:         p.Load.Duration,
		Shape:            p.Load.Shape,
		BurstPeriod:      p.Load.BurstPeriod,
		BurstLow:         p.Load.BurstLow,
		RejectRate:       p.Load.RejectRate,
		Links:            p.Load.Links,
		Timeout:          p.Load.Timeout,
		Retries:          p.Load.Retries,
		Hold:             p.Session.Hold,
		Interim:          p.Session.Interim,
		TrafficUp:        p.Traffic.Up.Min,
		TrafficUpMax:     p.Traffic.Up.Max,
		TrafficDown:      p.Traffic.Down.Min,
		TrafficDownMax:   p.Traffic.Down.Max,
		NASIdentifier:    p.NAS.Identifier,
		NASIPAddress:     p.NAS.IPAddress,
		NASPortType:      p.NAS.PortType,
		NASPortID:        p.NAS.PortID,
		CallingStationID: p.NAS.CallingStationID,
		Attributes:       p.Attributes,
		Dictionaries:     p.Dictionaries,
		CoAListen:        p.CoAListen,
		Seed:             p.Seed,
	}
}
