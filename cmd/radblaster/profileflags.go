package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/ispx-limited/radblaster/internal/profile"
)

// profileOverrides collects the flags a profile can fill in.
type profileOverrides struct {
	server, acct, user, access       *string
	rate                             *float64
	count                            *uint64
	duration, hold, interim          *time.Duration
	up, upMax, down, downMax         *uint64
	shape                            *string
	burstPeriod                      *time.Duration
	burstLow, rejectRate             *float64
	links, retries                   *int
	timeout                          *time.Duration
	nasID, nasIP, nasPortID, calling *string
	portType                         *uint
	coaListen                        *string
	seed                             *uint64
	attrs                            *stringList
	dicts                            *stringList
}

// applyProfile fills in every setting the profile carries, except where the
// command line already set it. A flag given explicitly always wins, so one
// saved estate can be aimed somewhere else without the file being edited.
func applyProfile(fs *flag.FlagSet, p *profile.Profile, o *profileOverrides) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	str := func(name string, dst *string, v string) {
		if !given[name] && v != "" {
			*dst = v
		}
	}
	dur := func(name string, dst *time.Duration, v string) {
		if given[name] || v == "" {
			return
		}
		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}
	u64 := func(name string, dst *uint64, v uint64) {
		if !given[name] && v != 0 {
			*dst = v
		}
	}
	f64 := func(name string, dst *float64, v float64) {
		if !given[name] && v != 0 {
			*dst = v
		}
	}
	num := func(name string, dst *int, v int) {
		if !given[name] && v != 0 {
			*dst = v
		}
	}

	str("server", o.server, p.Server)
	str("acct-server", o.acct, p.AcctServer)
	str("user", o.user, p.Username)
	str("access", o.access, p.Access)

	f64("rate", o.rate, p.Load.Rate)
	u64("count", o.count, p.Load.Count)
	dur("duration", o.duration, p.Load.Duration)
	dur("hold", o.hold, p.Session.Hold)
	dur("interim", o.interim, p.Session.Interim)

	u64("traffic-up", o.up, p.Traffic.Up.Min)
	u64("traffic-up-max", o.upMax, p.Traffic.Up.Max)
	u64("traffic-down", o.down, p.Traffic.Down.Min)
	u64("traffic-down-max", o.downMax, p.Traffic.Down.Max)

	str("shape", o.shape, p.Load.Shape)
	dur("burst-period", o.burstPeriod, p.Load.BurstPeriod)
	f64("burst-low", o.burstLow, p.Load.BurstLow)
	f64("reject-rate", o.rejectRate, p.Load.RejectRate)

	num("links", o.links, p.Load.Links)
	num("retries", o.retries, p.Load.Retries)
	dur("timeout", o.timeout, p.Load.Timeout)

	str("nas-id", o.nasID, p.NAS.Identifier)
	str("nas-ip", o.nasIP, p.NAS.IPAddress)
	str("nas-port-id", o.nasPortID, p.NAS.PortID)
	str("calling-station-id", o.calling, p.NAS.CallingStationID)
	if !given["nas-port-type"] && p.NAS.PortType != 0 {
		*o.portType = uint(p.NAS.PortType)
	}

	str("coa-listen", o.coaListen, p.CoAListen)
	u64("seed", o.seed, p.Seed)

	// A profile's dictionaries load before any given on the command line, so
	// the estate resolves its own attribute names without being told how.
	if len(p.Dictionaries) > 0 {
		*o.dicts = append(append(stringList{}, p.Dictionaries...), *o.dicts...)
	}

	// Attributes from the profile come first, so anything given on the command
	// line is added on top rather than replacing the estate's own.
	if len(p.Attributes) > 0 {
		merged := make(stringList, 0, len(p.Attributes)+len(*o.attrs))
		for _, a := range p.Attributes {
			merged = append(merged, fmt.Sprintf("%s=%s", a.Name, a.Value))
		}
		merged = append(merged, *o.attrs...)
		*o.attrs = merged
	}
}
