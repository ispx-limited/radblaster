package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample() *Profile {
	return &Profile{
		Name:        "nokia-business",
		Description: "Business tier on the lab BNG",
		Server:      "10.0.0.1:1812",
		AcctServer:  "10.0.0.1:1813",
		Access:      "ipoe",
		Username:    "sub-{n}@business.example.net",
		Load: Load{
			Rate: 500, Duration: "10m", Shape: "ramp",
			RejectRate: 0.02, Retries: 2, Timeout: "5s",
		},
		Session: Session{Hold: "30m", Interim: "300s"},
		Traffic: Traffic{
			Up:   Range{Min: 1_000_000, Max: 20_000_000},
			Down: Range{Min: 5_000_000, Max: 90_000_000},
		},
		NAS: NAS{Identifier: "bng-lab-01", IPAddress: "192.0.2.1", PortType: 15, PortID: "eth 1/1/{n}"},
		Attributes: []Attr{
			{Name: "Cisco-AVPair", Value: "ip:qos-policy-in=voice"},
			{Name: "26.3561.1", Value: "eth 1/1/{n}"},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	b, err := sample().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	want := sample()
	if got.Username != want.Username || got.Access != want.Access {
		t.Errorf("identity did not survive: %q/%q", got.Username, got.Access)
	}
	if got.Traffic.Up.Max != want.Traffic.Up.Max || got.Traffic.Down.Min != want.Traffic.Down.Min {
		t.Errorf("traffic did not survive: %+v", got.Traffic)
	}
	if len(got.Attributes) != 2 || got.Attributes[0].Name != "Cisco-AVPair" {
		t.Errorf("attributes did not survive: %+v", got.Attributes)
	}
	if got.Load.RejectRate != 0.02 || got.Load.Shape != "ramp" {
		t.Errorf("load did not survive: %+v", got.Load)
	}
}

// TestNoCredentialsAreWritten is the point of keeping a profile separate from a
// run: it is meant to be committed, and a credential in a file that is meant to
// be committed is a credential that will be.
func TestNoCredentialsAreWritten(t *testing.T) {
	b, err := sample().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := strings.ToLower(string(b))
	for _, word := range []string{"secret:", "password:"} {
		if strings.Contains(text, word) {
			t.Errorf("a profile carries %q", word)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := s.Put(sample()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "nokia-business" {
		t.Fatalf("List = %v, want one entry", names)
	}

	got, err := s.Get("nokia-business")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != sample().Username {
		t.Errorf("username = %q", got.Username)
	}

	if err := s.Delete("nokia-business"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("nokia-business"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestStoreRejectsEscapingNames matters because names arrive over HTTP: a name
// that walks out of the directory would let a caller read or overwrite files
// the tool has nothing to do with.
func TestStoreRejectsEscapingNames(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for _, name := range []string{
		"", "../escape", "sub/dir", "a b", "with.dot", "with:colon",
		strings.Repeat("x", 65), "..",
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.Put(&Profile{Name: name}); err == nil {
				t.Error("Put accepted the name")
			}
			if _, err := s.Get(name); err == nil {
				t.Error("Get accepted the name")
			}
			if err := s.Delete(name); err == nil {
				t.Error("Delete accepted the name")
			}
		})
	}

	// Nothing should have appeared outside the store.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("the store holds %d files after only rejected names", len(entries))
	}
}

// TestPutIsAtomic keeps a failed write from leaving a half-written profile
// where a good one used to be.
func TestPutIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Put(sample()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	updated := sample()
	updated.Username = "changed-{n}@example.net"
	if err := s.Put(updated); err != nil {
		t.Fatalf("Put again: %v", err)
	}

	got, err := s.Get("nokia-business")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != "changed-{n}@example.net" {
		t.Errorf("username = %q, want the update", got.Username)
	}

	// No temporary files should be left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the store holds %d files, want 1", len(entries))
	}
}

func TestLoadFileNamesFromThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lab-estate.yaml")

	p := sample()
	p.Name = ""
	b, _ := p.Marshal()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.Name != "lab-estate" {
		t.Errorf("name = %q, want it taken from the file name", got.Name)
	}
}

// TestMarshalledProfileIsReadable is worth pinning: a profile is meant to be
// read and edited by a person, and committed next to whatever else describes
// an estate.
func TestMarshalledProfileIsReadable(t *testing.T) {
	b, err := sample().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(b)

	if !strings.HasPrefix(text, "# radblaster profile") {
		t.Error("the file does not say what it is")
	}
	for _, want := range []string{"username: sub-{n}@business.example.net", "shape: ramp", "name: Cisco-AVPair"} {
		if !strings.Contains(text, want) {
			t.Errorf("the file does not contain %q", want)
		}
	}
}
