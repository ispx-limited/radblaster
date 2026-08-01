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

// TestDiscoverHandlesAnInstalledTree covers the layout an installation
// actually leaves behind: a directory holding one subtree per protocol, not a
// single file.
func TestDiscoverHandlesAnInstalledTree(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "dictionary")
	radiusDir := filepath.Join(installed, "radius")
	dhcpDir := filepath.Join(installed, "dhcpv4")
	for _, d := range []string{radiusDir, dhcpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	write(filepath.Join(radiusDir, "dictionary"),
		"BEGIN PROTOCOL RADIUS 1\n$INCLUDE dictionary.site\n")
	write(filepath.Join(radiusDir, "dictionary.site"),
		"ATTRIBUTE Site-Tag 200 string\n")
	// Another protocol reusing low attribute numbers, which must not be
	// pulled in: its option 1 is not User-Name.
	write(filepath.Join(dhcpDir, "dictionary"),
		"BEGIN PROTOCOL DHCPv4 4\nATTRIBUTE Option-Request 1 octets\n")

	old := searchPaths
	searchPaths = []string{installed}
	t.Cleanup(func() { searchPaths = old })

	found := DiscoverDictionaries()
	if len(found) != 1 {
		t.Fatalf("found %v, want the RADIUS manifest", found)
	}
	if filepath.Base(filepath.Dir(found[0])) != "radius" {
		t.Errorf("found %q, want the manifest inside the radius subtree", found[0])
	}

	d, _, err := LoadDiscovered()
	if err != nil {
		t.Fatalf("LoadDiscovered: %v", err)
	}
	if d.ByName("Site-Tag") == nil {
		t.Error("the RADIUS dictionary was not loaded")
	}
	if d.ByName("Option-Request") != nil {
		t.Error("another protocol's attributes leaked into the RADIUS namespace")
	}
	if def := d.ByNumber(1); def == nil || def.Name != "User-Name" {
		t.Errorf("attribute 1 is %v, want User-Name; another protocol overwrote it", def)
	}
}

// TestLoadIgnoresOtherProtocols guards the namespace however the file is
// reached, including someone pointing at the tree root by hand.
func TestLoadIgnoresOtherProtocols(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dictionary.dhcpv4")
	body := "BEGIN PROTOCOL DHCPv4 4\nATTRIBUTE Option-Request 1 octets\nATTRIBUTE Subnet-Mask 2 ipaddr\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	d, err := LoadDictionary(path)
	if err != nil {
		t.Fatalf("LoadDictionary: %v", err)
	}
	if d.ByName("Option-Request") != nil {
		t.Error("a DHCP attribute was loaded into the RADIUS namespace")
	}
	if def := d.ByNumber(2); def == nil || def.Name != "User-Password" {
		t.Errorf("attribute 2 is %v, want User-Password", def)
	}
}

// TestDiscoverSkipsADirectoryWithNoRadiusSubtree keeps discovery from guessing
// at a directory it does not recognise.
func TestDiscoverSkipsADirectoryWithNoRadiusSubtree(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "dictionary")
	if err := os.MkdirAll(filepath.Join(empty, "tacacs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := searchPaths
	searchPaths = []string{empty}
	t.Cleanup(func() { searchPaths = old })

	if found := DiscoverDictionaries(); len(found) != 0 {
		t.Errorf("found %v, want none", found)
	}
}
