package engine

import (
	"fmt"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// materialSlip is how far behind schedule a run has to fall before it is worth
// mentioning. Sub-millisecond slippage is timer granularity rather than the
// generator struggling, and saying so every run would teach people to ignore
// the notes.
const materialSlip = 2 * time.Millisecond

// Notes returns the plain-language reasons a result means less than it appears
// to.
//
// These live with the result rather than with whichever interface is showing
// it, so the command line and the browser cannot end up disagreeing about what
// a run means. Silence about a caveat would be worse than a smaller number.
func (r *Result) Notes() []string {
	m := r.Access()
	var notes []string

	var blocked, timeouts, sent uint64
	for _, e := range r.Exchanges {
		blocked += e.Blocked
		timeouts += e.Timeouts
		sent += e.Sent
	}

	// A blocked request means every identifier on its link was already
	// outstanding, but the reason matters and the remedy is opposite in the
	// two cases. If replies are coming back we simply did not open enough
	// links. If they are timing out, identifiers are being held by requests
	// the server never answered, and opening more links would only push
	// harder at something already failing.
	lossy := sent > 0 && float64(timeouts)/float64(sent) > 0.01

	switch {
	case blocked > 0 && lossy:
		notes = append(notes, fmt.Sprintf(
			"%d requests could not be offered, but that is a symptom rather than the problem: "+
				"identifiers stayed in use because %d requests went unanswered. The server is the "+
				"limit here, so more links would only push harder at something already dropping.",
			blocked, timeouts))

	case blocked > 0:
		// Concurrency-bound. The ceiling follows from how many requests can be
		// in flight at once and how long each one occupies its slot, so state
		// both rather than just advising more links.
		capacity := r.Links * radius.IDSpace
		note := fmt.Sprintf(
			"%d requests could not be offered: %d links carry at most %d requests at once.",
			blocked, r.Links, capacity)
		if mean := m.ServiceTime.Mean(); mean > 0 {
			ceiling := float64(capacity) / (mean / float64(time.Second))
			note += fmt.Sprintf(
				" At a mean server response of %s that caps this run at about %.0f/s regardless of "+
					"the rate asked for. Raise the link count to lift the ceiling.",
				formatDuration(uint64(mean)), ceiling)
		} else {
			note += " Raise the link count to lift the ceiling."
		}
		notes = append(notes, note)
	}

	for _, e := range r.Exchanges {
		if e.Retransmits > 0 && e.Sent > 0 {
			notes = append(notes, fmt.Sprintf(
				"%s needed %d retransmissions across %d requests. Retries hide loss in the success "+
					"rate, so this is the figure that shows the path or the server dropping packets.",
				e.Name, e.Retransmits, e.Sent))
		}
		if e.Timeouts > 0 {
			notes = append(notes, fmt.Sprintf(
				"%d of %d %s requests (%.1f%%) were never answered.",
				e.Timeouts, e.Sent, e.Name, 100*float64(e.Timeouts)/float64(e.Sent)))
		}
		if e.Unverified > 0 {
			notes = append(notes, fmt.Sprintf(
				"%d %s replies failed verification, which almost always means the shared secret is wrong.",
				e.Unverified, e.Name))
		}
	}

	if d := r.DynAuth; d.DisconnectNAKs+d.CoANAKs > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d server-initiated requests were refused because the session they named was no longer "+
				"online. For a bulk change that is the figure that matters: those subscribers did not "+
				"receive it.", d.DisconnectNAKs+d.CoANAKs))
	}
	if r.DynAuth.Unverified > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d server-initiated requests failed verification and were dropped without a reply, "+
				"which means they were signed with a different shared secret.", r.DynAuth.Unverified))
	}

	if r.OfferedRate > 0 && r.AchievedRate < r.OfferedRate*0.95 {
		notes = append(notes, fmt.Sprintf(
			"Achieved %.0f sessions/s against an offered %.0f/s, so the server was tested at less "+
				"load than asked for.", r.AchievedRate, r.OfferedRate))
	}

	if m.Latency.Count() > 0 {
		scheduled := m.Latency.Quantile(0.5)
		service := m.ServiceTime.Quantile(0.5)
		slip := time.Duration(scheduled - service)
		if scheduled > service && slip > materialSlip && scheduled > service*2 {
			notes = append(notes, fmt.Sprintf(
				"Requests went out a median of %s later than scheduled, so the latency measured "+
					"from the scheduled send (%s) is mostly this run failing to keep pace rather "+
					"than the server being slow (%s from the actual send).",
				formatDuration(uint64(slip)), formatDuration(scheduled), formatDuration(service)))
		}
	}

	return notes
}

func formatDuration(ns uint64) string {
	return time.Duration(ns).Round(time.Microsecond).String()
}
