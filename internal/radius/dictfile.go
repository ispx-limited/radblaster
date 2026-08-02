package radius

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxIncludeDepth stops a dictionary that includes itself, directly or through
// a chain, from recursing until it runs out of stack.
const maxIncludeDepth = 16

// Clone returns a copy that can be extended without disturbing the original.
// The shared standard dictionary is never mutated.
func (d *Dictionary) Clone() *Dictionary {
	out := &Dictionary{
		byName:     make(map[string]*AttrDef, len(d.byName)),
		vendorIDs:  make(map[string]uint32, len(d.vendorIDs)),
		byVendor:   make(map[uint32]map[AttrType]*AttrDef, len(d.byVendor)),
		vendorName: make(map[uint32]string, len(d.vendorName)),
	}
	// An array, so this copies rather than aliases: loading into the clone
	// must not reach back into the shared standard dictionary.
	out.byNumber = d.byNumber
	for k, v := range d.byName {
		out.byName[k] = v
	}
	for k, v := range d.vendorIDs {
		out.vendorIDs[k] = v
	}
	for k, v := range d.vendorName {
		out.vendorName[k] = v
	}
	for vendor, attrs := range d.byVendor {
		m := make(map[AttrType]*AttrDef, len(attrs))
		for k, v := range attrs {
			m[k] = v
		}
		out.byVendor[vendor] = m
	}
	return out
}

// LoadDictionary reads dictionary definitions from the given files or
// directories and returns a dictionary containing them on top of the built-in
// RFC attributes.
//
// The format is the widely used one: ATTRIBUTE, VALUE, VENDOR, BEGIN-VENDOR,
// END-VENDOR and $INCLUDE, one directive per line, with # starting a comment.
// Nothing is shipped with radblaster beyond the RFC set, so point this at the
// dictionary directory your own RADIUS server already uses and both ends will
// resolve the same names to the same numbers.
func LoadDictionary(paths ...string) (*Dictionary, error) {
	d := StandardDictionary().Clone()
	seen := make(map[string]bool)
	for _, p := range paths {
		if err := d.loadPath(p, 0, seen); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func (d *Dictionary) loadPath(path string, depth int, seen map[string]bool) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("radius: dictionary includes nested more than %d deep at %s", maxIncludeDepth, path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("radius: reading dictionary %s: %w", path, err)
	}
	if !info.IsDir() {
		return d.loadFile(path, depth, seen)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("radius: reading dictionary directory %s: %w", path, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := d.loadFile(filepath.Join(path, e.Name()), depth, seen); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dictionary) loadFile(path string, depth int, seen map[string]bool) error {
	// A dictionary set includes the same file from several places, and every
	// $INCLUDE used to read it again from scratch. That makes the work a tree
	// rather than a walk, so a handful of small files including each other a
	// few times over expands into billions of parses and exhausts the machine
	// before it finishes. Reading each file once is also what the servers these
	// dictionaries come from do, and it stops a VALUE line being applied
	// repeatedly by the same file arriving twice.
	key := resolvedPath(path)
	if seen[key] {
		return nil
	}
	seen[key] = true

	// Stat before opening: a pipe in a dictionary directory is not a
	// dictionary, and opening one blocks for as long as nothing writes to it.
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("radius: reading dictionary %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("radius: reading dictionary %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return d.parse(f, path, depth, seen)
}

// resolvedPath names a file the same way however it was reached, so that one
// file included by two different routes is recognised as one file.
func resolvedPath(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	if p, err := filepath.Abs(path); err == nil {
		return p
	}
	return path
}

// parse reads one dictionary file. Directives it does not recognise are
// skipped rather than rejected: real dictionary sets carry constructs beyond
// this subset, and refusing to load a whole file over one unfamiliar line
// would make the feature useless in practice.
func (d *Dictionary) parse(r io.Reader, path string, depth int, seen map[string]bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var vendor uint32
	line := 0

	for sc.Scan() {
		line++
		text := sc.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}

		switch strings.ToUpper(fields[0]) {
		case "BEGIN":
			// A dictionary set covers several protocols, each numbering its
			// attributes independently. Loading another protocol's file would
			// put its option 1 in the same namespace as User-Name, so a file
			// that declares itself as anything but RADIUS is skipped whole.
			if len(fields) >= 3 && strings.EqualFold(fields[1], "PROTOCOL") &&
				!strings.EqualFold(fields[2], "RADIUS") {
				return nil
			}

		case "$INCLUDE":
			if len(fields) < 2 {
				return dictErr(path, line, "$INCLUDE needs a file")
			}
			inc := fields[1]
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(filepath.Dir(path), inc)
			}
			if err := d.loadPath(inc, depth+1, seen); err != nil {
				return err
			}

		case "VENDOR":
			if len(fields) < 3 {
				return dictErr(path, line, "VENDOR needs a name and a number")
			}
			id, err := parseNumber(fields[2])
			if err != nil {
				return dictErr(path, line, "VENDOR number: "+err.Error())
			}
			d.addVendor(fields[1], uint32(id))

		case "BEGIN-VENDOR":
			if len(fields) < 2 {
				return dictErr(path, line, "BEGIN-VENDOR needs a name")
			}
			id, ok := d.vendorIDs[strings.ToLower(fields[1])]
			if !ok {
				return dictErr(path, line, "BEGIN-VENDOR names an undeclared vendor "+asField(fields[1]))
			}
			vendor = id

		case "END-VENDOR":
			vendor = 0

		case "ATTRIBUTE":
			if len(fields) < 4 {
				return dictErr(path, line, "ATTRIBUTE needs a name, a number and a type")
			}
			num, err := parseNumber(fields[2])
			if err != nil {
				// Nested or OID-style numbers belong to constructs this
				// subset does not model; skip rather than fail the file.
				continue
			}
			if num > 255 {
				continue
			}
			def := &AttrDef{
				Type:   AttrType(num),
				Vendor: vendor,
				Name:   fields[1],
				Data:   dataTypeOf(fields[3]),
			}
			if len(fields) > 4 {
				applyFlags(def, fields[4])
			}
			d.addAttr(def)

		case "VALUE":
			if len(fields) < 4 {
				return dictErr(path, line, "VALUE needs an attribute, a name and a number")
			}
			def := d.resolve(vendor, fields[1])
			if def == nil {
				continue // a value for an attribute this subset skipped
			}
			num, err := parseNumber(fields[3])
			if err != nil {
				continue
			}
			def.Values = append(def.Values, Value{Name: fields[2], Number: uint32(num)})
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("radius: reading dictionary %s: %w", path, err)
	}
	return nil
}

func dictErr(path string, line int, msg string) error {
	return fmt.Errorf("radius: %s line %d: %s", path, line, msg)
}

// asField prepares a token from a file to be quoted in an error message.
//
// Pointing this at something that is not a dictionary should say so without
// reprinting the file: a long line, or one carrying something private, has no
// business being repeated at length in an error an operator may well paste
// somewhere else.
func asField(s string) string {
	const max = 40
	if len(s) > max {
		s = s[:max] + "..."
	}
	return strconv.Quote(s)
}

// parseNumber accepts decimal and hexadecimal, which dictionaries mix freely.
func parseNumber(s string) (uint64, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 32)
	}
	return strconv.ParseUint(s, 10, 32)
}

// dataTypeOf maps a declared type to its wire representation. Types outside
// the subset are carried as opaque octets, which round-trips them correctly
// even though their values cannot be formatted meaningfully.
func dataTypeOf(s string) DataType {
	switch strings.ToLower(s) {
	case "string", "text":
		return TypeString
	case "ipaddr":
		return TypeIPAddr
	case "ipv6addr":
		return TypeIPv6Addr
	case "ipv6prefix":
		return TypeIPv6Prefix
	case "integer", "uint32", "int32", "signed":
		return TypeInteger
	case "byte", "uint8":
		return TypeByte
	case "short", "uint16":
		return TypeShort
	case "date":
		return TypeDate
	default:
		return TypeOctets
	}
}

func applyFlags(def *AttrDef, flags string) {
	for _, f := range strings.Split(flags, ",") {
		switch strings.TrimSpace(strings.ToLower(f)) {
		case "has_tag":
			def.HasTag = true
		case "encrypt=1", "encrypt=user-password":
			def.Encrypt = EncryptionUserPassword
		}
	}
}

// addVendor records a vendor identifier under its name.
func (d *Dictionary) addVendor(name string, id uint32) {
	d.vendorIDs[strings.ToLower(name)] = id
	if _, ok := d.vendorName[id]; !ok {
		d.vendorName[id] = name
	}
}

// addAttr records an attribute definition. A later definition replaces an
// earlier one of the same name, which is how dictionary sets layer overrides.
//
// A vendor attribute is never registered under its bare name. Dictionaries
// differ on whether they write it qualified ("Cisco-AVPair") or bare
// ("AVPair" inside BEGIN-VENDOR Cisco), and the bare style collides: Cisco
// attribute 2 is called NAS-Port, which is also standard attribute 5. Keeping
// the bare name out of the shared namespace stops a vendor attribute from
// silently shadowing a standard one of the same name.
func (d *Dictionary) addAttr(def *AttrDef) {
	if def.Vendor == 0 {
		def.Qualified = def.Name
		d.byName[strings.ToLower(def.Name)] = def
		// Also by number, or a loaded attribute would be findable by name but
		// invisible to anything decoding a packet, and its declared type would
		// be lost when a value came to be formatted.
		d.byNumber[def.Type] = def
		return
	}

	attrs, ok := d.byVendor[def.Vendor]
	if !ok {
		attrs = make(map[AttrType]*AttrDef)
		d.byVendor[def.Vendor] = attrs
	}
	attrs[def.Type] = def

	vendor := d.vendorName[def.Vendor]
	if vendor == "" {
		return
	}

	// The qualified form is always available, and a friendly "Vendor-Name"
	// form matches what operators have in their configuration. A dictionary
	// that already qualifies the name keeps it as written.
	d.byName[strings.ToLower(vendor+"."+def.Name)] = def

	friendly := def.Name
	if !strings.HasPrefix(strings.ToLower(friendly), strings.ToLower(vendor)+"-") {
		friendly = vendor + "-" + def.Name
	}
	def.Qualified = friendly
	d.byName[strings.ToLower(friendly)] = def
}

// resolve finds an attribute by the name a dictionary file used, which inside
// a BEGIN-VENDOR block may be bare rather than qualified.
func (d *Dictionary) resolve(vendor uint32, name string) *AttrDef {
	if vendor != 0 {
		if v := d.vendorName[vendor]; v != "" {
			if def := d.ByName(v + "." + name); def != nil {
				return def
			}
		}
	}
	return d.ByName(name)
}

// VendorID returns the identifier declared for a vendor name.
func (d *Dictionary) VendorID(name string) (uint32, bool) {
	id, ok := d.vendorIDs[strings.ToLower(name)]
	return id, ok
}

// VendorName returns the name declared for a vendor identifier.
func (d *Dictionary) VendorName(id uint32) string { return d.vendorName[id] }

// ByVendor returns the definition of a vendor attribute, or nil if unknown.
func (d *Dictionary) ByVendor(vendor uint32, t AttrType) *AttrDef {
	if attrs, ok := d.byVendor[vendor]; ok {
		return attrs[t]
	}
	return nil
}
