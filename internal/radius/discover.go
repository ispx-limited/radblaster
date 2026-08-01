package radius

import (
	"os"
	"path/filepath"
)

// wellKnownDictionaries are the places a RADIUS installation usually leaves
// its dictionaries. The list exists so an operator does not have to tell
// radblaster where to find what is already on the machine.
//
// Discovery only makes names resolvable; it never changes what goes on the
// wire, because an attribute is addressable by number regardless. What was
// found is always reported, and saving a profile pins the paths explicitly, so
// a run that has to be repeatable does not depend on what happens to be
// installed on the host that ran it.
var wellKnownDictionaries = []string{
	"/usr/share/freeradius/dictionary",
	"/usr/local/share/freeradius/dictionary",
	"/usr/share/radius/dictionary",
	"/usr/local/share/radius/dictionary",
	"/etc/raddb/dictionary",
	"/usr/local/etc/raddb/dictionary",
	"/opt/homebrew/share/freeradius/dictionary",
}

// searchPaths is the list discovery walks. It is a variable so a test can
// point it somewhere real rather than depending on what is installed.
var searchPaths = wellKnownDictionaries

// DiscoverDictionaries returns the well-known dictionary paths that exist on
// this machine, most conventional first.
func DiscoverDictionaries() []string {
	var found []string
	for _, p := range searchPaths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			// Absolute, so a path written into a profile still resolves when
			// that profile is run from somewhere else.
			if a, err := filepath.Abs(p); err == nil {
				p = a
			}
			found = append(found, p)
		}
	}
	return found
}

// LoadDiscovered loads whatever dictionaries are already on the machine,
// returning the paths it used so a caller can report them and a profile can
// record them.
func LoadDiscovered() (*Dictionary, []string, error) {
	paths := DiscoverDictionaries()
	if len(paths) == 0 {
		return StandardDictionary(), nil, nil
	}
	d, err := LoadDictionary(paths...)
	if err != nil {
		return nil, nil, err
	}
	return d, paths, nil
}
