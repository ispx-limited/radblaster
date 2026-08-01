package radius

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverFindsWhatIsInstalled(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "dictionary")
	if err := os.WriteFile(present, []byte("ATTRIBUTE Site-Tag 200 string\n"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	old := searchPaths
	searchPaths = []string{filepath.Join(dir, "missing"), present}
	t.Cleanup(func() { searchPaths = old })

	found := DiscoverDictionaries()
	if len(found) != 1 {
		t.Fatalf("found %v, want just the one that exists", found)
	}
	if !filepath.IsAbs(found[0]) {
		t.Errorf("found %q, want an absolute path so it still resolves from elsewhere", found[0])
	}

	d, paths, err := LoadDiscovered()
	if err != nil {
		t.Fatalf("LoadDiscovered: %v", err)
	}
	if len(paths) != 1 {
		t.Errorf("paths = %v, want one", paths)
	}
	if d.ByName("Site-Tag") == nil {
		t.Error("the discovered dictionary was not loaded")
	}
	// The built-in set must survive discovery.
	if d.ByName("User-Name") == nil {
		t.Error("the standard attributes were lost")
	}
}

func TestDiscoverFindsNothingWhenNothingIsInstalled(t *testing.T) {
	old := searchPaths
	searchPaths = []string{filepath.Join(t.TempDir(), "absent")}
	t.Cleanup(func() { searchPaths = old })

	if found := DiscoverDictionaries(); len(found) != 0 {
		t.Errorf("found %v, want none", found)
	}

	d, paths, err := LoadDiscovered()
	if err != nil {
		t.Fatalf("LoadDiscovered: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want none", paths)
	}
	if d.ByName("User-Name") == nil {
		t.Error("the built-in set should still be available")
	}
}

// TestDiscoverIgnoresDirectories guards against picking up a directory that
// happens to sit where a dictionary file usually does.
func TestDiscoverIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	asDir := filepath.Join(dir, "dictionary")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := searchPaths
	searchPaths = []string{asDir}
	t.Cleanup(func() { searchPaths = old })

	if found := DiscoverDictionaries(); len(found) != 0 {
		t.Errorf("found %v, want none", found)
	}
}

func TestSearch(t *testing.T) {
	d := loadTestDict(t)

	t.Run("prefix matches come first", func(t *testing.T) {
		got := d.Search("example-", 20)
		if len(got) == 0 {
			t.Fatal("no matches")
		}
		for _, m := range got {
			if m.Vendor != exampleVendor {
				t.Errorf("%q is not an Example attribute", m.Name)
			}
		}
	})

	t.Run("standard attributes are findable", func(t *testing.T) {
		got := d.Search("user-na", 10)
		if len(got) == 0 || got[0].Name != "User-Name" {
			t.Errorf("= %v, want User-Name first", got)
		}
	})

	t.Run("named values come with the attribute", func(t *testing.T) {
		for _, m := range d.Search("service-type", 5) {
			if m.Name != "Service-Type" {
				continue
			}
			if len(m.Values) == 0 {
				t.Fatal("Service-Type came back with no named values to pick from")
			}
			return
		}
		t.Fatal("Service-Type was not found")
	})

	t.Run("the limit is honoured", func(t *testing.T) {
		if got := d.Search("", 3); len(got) != 3 {
			t.Errorf("got %d matches, want 3", len(got))
		}
	})

	t.Run("an unknown name matches nothing", func(t *testing.T) {
		if got := d.Search("no-such-attribute-anywhere", 10); len(got) != 0 {
			t.Errorf("got %v, want none", got)
		}
	})
}

func TestCount(t *testing.T) {
	attrs, vendors := loadTestDict(t).Count()
	if attrs < len(standardAttrs) {
		t.Errorf("counted %d attributes, want at least the %d built in", attrs, len(standardAttrs))
	}
	if vendors < 2 {
		t.Errorf("counted %d vendors, want the two the fixtures declare", vendors)
	}
}

// TestSearchOffersNamesThatResolve is the property the interface depends on:
// every suggestion must be something a caller can actually type. A bare vendor
// name would not parse, and two vendors' identically named attributes would be
// indistinguishable in a list.
func TestSearchOffersNamesThatResolve(t *testing.T) {
	d := loadTestDict(t)

	for _, q := range []string{"", "avpair", "circuit", "example", "user"} {
		for _, m := range d.Search(q, 50) {
			if d.ByName(m.Name) == nil {
				t.Errorf("Search(%q) offered %q, which does not resolve", q, m.Name)
			}
			if m.Vendor != 0 {
				spec, err := d.ParseSpec(m.Name + "=x")
				if err != nil {
					t.Errorf("Search(%q) offered %q, which does not parse: %v", q, m.Name, err)
					continue
				}
				if spec.Vendor != m.Vendor {
					t.Errorf("%q resolved to vendor %d, want %d", m.Name, spec.Vendor, m.Vendor)
				}
			}
		}
	}
}

// TestSearchDistinguishesVendorsSharingAName guards the case that made the
// list useless: two vendors both defining Agent-Circuit-Id.
func TestSearchDistinguishesVendorsSharingAName(t *testing.T) {
	d := loadTestDict(t)

	seen := map[string]bool{}
	for _, m := range d.Search("", 200) {
		if seen[m.Name] {
			t.Errorf("%q appears more than once, so the list cannot tell them apart", m.Name)
		}
		seen[m.Name] = true
	}
}
