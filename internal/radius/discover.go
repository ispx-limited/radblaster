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
// wire. A dictionary is a local convenience that maps names to numbers, and
// the names themselves are never exchanged between a server and its clients,
// so an attribute is addressable by number whatever is loaded. What was found
// is always reported, and saving a profile pins the paths explicitly, so a run
// that has to be repeatable does not depend on what happens to be installed on
// the host that ran it.
// The order matters and is not alphabetical. The shared files come first and
// the configuration directory last, because a later definition replaces an
// earlier one: an operator's own attributes live in the configuration
// directory and should win over the shipped set, which is not meant to be
// edited. The /usr/local variants are where a build from source installs by
// default, and the plain /usr ones where a package does.
var wellKnownDictionaries = []string{
	"/usr/share/freeradius/dictionary",
	"/usr/local/share/freeradius/dictionary",
	"/usr/share/radius/dictionary",
	"/usr/local/share/radius/dictionary",
	"/opt/homebrew/share/freeradius/dictionary",
	"/etc/raddb/dictionary",
	"/usr/local/etc/raddb/dictionary",
}

// searchPaths is the list discovery walks. It is a variable so a test can
// point it somewhere real rather than depending on what is installed.
var searchPaths = wellKnownDictionaries

// radiusManifest is where the RADIUS protocol's own manifest sits inside an
// installed dictionary tree.
const radiusManifest = "radius/dictionary"

// DiscoverDictionaries returns a loadable dictionary for each well-known
// location present on this machine, most conventional first.
//
// A location may be either a file or a directory depending on how the
// installation lays them out. When it is a directory it holds one subtree per
// protocol, and only the RADIUS one may be loaded: the protocols number their
// attributes independently, so pulling in the others would put a DHCP option 1
// in the same namespace as User-Name. The manifest inside that subtree is
// preferred over the files beside it, because it includes them in a deliberate
// order that decides which definition wins.
func DiscoverDictionaries() []string {
	var found []string
	for _, p := range searchPaths {
		resolved, ok := resolveDictionary(p)
		if !ok {
			continue
		}
		// Absolute, so a path written into a profile still resolves when that
		// profile is run from somewhere else.
		if a, err := filepath.Abs(resolved); err == nil {
			resolved = a
		}
		found = append(found, resolved)
	}
	return found
}

// resolveDictionary turns a candidate location into something safe to load.
func resolveDictionary(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		return path, true
	}
	manifest := filepath.Join(path, radiusManifest)
	if mi, err := os.Stat(manifest); err == nil && !mi.IsDir() {
		return manifest, true
	}
	// A directory with no RADIUS subtree is not something to guess at.
	return "", false
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
