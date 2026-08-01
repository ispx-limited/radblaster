// Package profile stores what a run should look like, apart from its secrets.
//
// A profile is a file. The interface edits it, the command line runs it, and
// it is meant to be committed alongside whatever else describes an estate, so
// a test that ran during a maintenance window can be run again the same way
// six months later.
package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Attr is one extra attribute a run puts on every request.
type Attr struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value" json:"value"`
}

// Range is a span each subscriber draws its own value from. A Max at or below
// Min means every subscriber gets Min.
type Range struct {
	Min uint64 `yaml:"min" json:"min"`
	Max uint64 `yaml:"max,omitempty" json:"max"`
}

// Profile describes an estate and the load offered to it.
//
// It deliberately holds no shared secret and no password. A profile is meant
// to be committed, and a credential in a file that is meant to be committed is
// a credential that will be. Those are supplied when a run starts, from the
// environment or from the interface, and are never written back.
type Profile struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	Server     string `yaml:"server,omitempty" json:"server,omitempty"`
	AcctServer string `yaml:"acctServer,omitempty" json:"acctServer,omitempty"`

	// Access is pap, chap or ipoe.
	Access   string `yaml:"access,omitempty" json:"access,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`

	Load    Load    `yaml:"load,omitempty" json:"load"`
	Session Session `yaml:"session,omitempty" json:"session"`
	Traffic Traffic `yaml:"traffic,omitempty" json:"traffic"`
	NAS     NAS     `yaml:"nas,omitempty" json:"nas"`

	Attributes []Attr `yaml:"attributes,omitempty" json:"attributes,omitempty"`

	// Dictionaries are the files a profile's attribute names resolve through.
	// Naming them here is what lets a profile run on its own: an estate that
	// uses vendor attributes is not fully described without saying which
	// dictionary gives those names meaning.
	Dictionaries []string `yaml:"dictionaries,omitempty" json:"dictionaries,omitempty"`

	CoAListen string `yaml:"coaListen,omitempty" json:"coaListen,omitempty"`
	Seed      uint64 `yaml:"seed,omitempty" json:"seed,omitempty"`
}

// Load is how much is offered and in what shape.
type Load struct {
	Rate     float64 `yaml:"rate,omitempty" json:"rate,omitempty"`
	Count    uint64  `yaml:"count,omitempty" json:"count,omitempty"`
	Duration string  `yaml:"duration,omitempty" json:"duration,omitempty"`

	Shape       string  `yaml:"shape,omitempty" json:"shape,omitempty"`
	BurstPeriod string  `yaml:"burstPeriod,omitempty" json:"burstPeriod,omitempty"`
	BurstLow    float64 `yaml:"burstLow,omitempty" json:"burstLow,omitempty"`

	// RejectRate is the share of sessions given a username no server knows.
	RejectRate float64 `yaml:"rejectRate,omitempty" json:"rejectRate,omitempty"`

	Links   int    `yaml:"links,omitempty" json:"links,omitempty"`
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries int    `yaml:"retries" json:"retries"`
}

// Session is how a subscriber behaves once it is online.
type Session struct {
	Hold    string `yaml:"hold,omitempty" json:"hold,omitempty"`
	Interim string `yaml:"interim,omitempty" json:"interim,omitempty"`
}

// Traffic is the octets per second a subscriber claims to have carried.
type Traffic struct {
	Up   Range `yaml:"up,omitempty" json:"up"`
	Down Range `yaml:"down,omitempty" json:"down"`
}

// NAS is what the simulated access node calls itself.
type NAS struct {
	Identifier       string `yaml:"identifier,omitempty" json:"identifier,omitempty"`
	IPAddress        string `yaml:"ipAddress,omitempty" json:"ipAddress,omitempty"`
	PortType         uint32 `yaml:"portType,omitempty" json:"portType,omitempty"`
	PortID           string `yaml:"portId,omitempty" json:"portId,omitempty"`
	CallingStationID string `yaml:"callingStationId,omitempty" json:"callingStationId,omitempty"`
}

// Marshal renders a profile as YAML.
func (p *Profile) Marshal() ([]byte, error) {
	var b strings.Builder
	b.WriteString("# radblaster profile\n")
	b.WriteString("# Secrets are deliberately absent. Supply them when the run starts.\n")

	out, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("profile: writing %q: %w", p.Name, err)
	}
	b.Write(out)
	return []byte(b.String()), nil
}

// Unmarshal parses a profile.
func Unmarshal(b []byte) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	return &p, nil
}

// ErrNotFound is returned when a profile does not exist.
var ErrNotFound = errors.New("profile: not found")

// Store is a directory of profiles.
type Store struct{ dir string }

// NewStore returns a store over dir, creating it if it is missing.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("profile: creating %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the directory profiles are kept in.
func (s *Store) Dir() string { return s.dir }

// validName keeps a name to something that is safe as a file name and cannot
// escape the directory, since names arrive over HTTP.
func validName(name string) error {
	if name == "" {
		return errors.New("profile: a name is required")
	}
	if len(name) > 64 {
		return errors.New("profile: the name is too long")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return fmt.Errorf("profile: %q may only contain letters, digits, hyphens and underscores", name)
		}
	}
	return nil
}

func (s *Store) path(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, name+".yaml"), nil
}

// List returns every profile in the store, by name.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("profile: reading %s: %w", s.dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names, nil
}

// Get reads a profile.
func (s *Store) Get(name string) (*Profile, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("profile: reading %s: %w", name, err)
	}
	p, err := Unmarshal(b)
	if err != nil {
		return nil, err
	}
	p.Name = name
	return p, nil
}

// Put writes a profile, replacing any of the same name.
func (s *Store) Put(p *Profile) error {
	path, err := s.path(p.Name)
	if err != nil {
		return err
	}
	b, err := p.Marshal()
	if err != nil {
		return err
	}
	// Write beside the target and rename, so a failure partway through cannot
	// leave a half-written profile where a good one used to be.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("profile: writing %s: %w", p.Name, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("profile: writing %s: %w", p.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("profile: writing %s: %w", p.Name, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("profile: writing %s: %w", p.Name, err)
	}
	return nil
}

// Delete removes a profile.
func (s *Store) Delete(name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return fmt.Errorf("profile: removing %s: %w", name, err)
	}
	return nil
}

// LoadFile reads a profile from an explicit path, which is how the command
// line runs one that lives alongside a project rather than in the store.
func LoadFile(path string) (*Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profile: reading %s: %w", path, err)
	}
	p, err := Unmarshal(b)
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		p.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return p, nil
}
