package main

import (
	"fmt"
	"os"

	"github.com/ispx-limited/radblaster/internal/radius"
)

// loadDictionaries resolves the dictionaries a run should use.
//
// Explicit paths always win. Otherwise the usual system locations are checked,
// because on a machine that already runs RADIUS the dictionaries are right
// there and asking for them again is friction for nothing. Discovery only
// makes names resolvable, never changing what goes on the wire, and the paths
// used are always reported so a result is never quietly dependent on what
// happened to be installed.
func loadDictionaries(explicit []string, noAuto bool) (*radius.Dictionary, []string, error) {
	if len(explicit) > 0 {
		d, err := radius.LoadDictionary(explicit...)
		return d, explicit, err
	}
	if noAuto {
		return radius.StandardDictionary(), nil, nil
	}
	return radius.LoadDiscovered()
}

// reportDictionaries says what was loaded and from where, so an operator can
// see that a name resolved because of a file on this machine.
func reportDictionaries(d *radius.Dictionary, paths []string) {
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "Using the built-in RFC attributes only. Vendor attributes can be written by number.")
		return
	}
	attrs, vendors := d.Count()
	fmt.Fprintf(os.Stderr, "Loaded %d attributes across %d vendors from:\n", attrs, vendors)
	for _, p := range paths {
		fmt.Fprintf(os.Stderr, "  %s\n", p)
	}
}
