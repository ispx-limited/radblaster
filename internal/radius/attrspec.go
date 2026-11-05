package radius

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// AttrSpec is an attribute a caller asked for, resolved to its wire identity
// but with the value left as written.
//
// The value is kept as text because it may be a template rendered per session,
// so it cannot be encoded until the session it belongs to is known.
type AttrSpec struct {
	Vendor uint32
	Type   AttrType
	Def    *AttrDef
	Value  string
	// Name is what the caller wrote, kept for error messages.
	Name string
}

// ParseSpec resolves an attribute written as "Name=value".
//
// The name may be a dictionary name, a plain attribute number, or a
// vendor attribute written as "26.<vendor>.<type>" or "<vendor>.<type>". The
// numeric forms mean a dictionary is never required to put something on the
// wire, which matters because radblaster ships no vendor dictionaries.
func (d *Dictionary) ParseSpec(s string) (AttrSpec, error) {
	name, value, ok := strings.Cut(s, "=")
	if !ok {
		return AttrSpec{}, fmt.Errorf("radius: attribute %q is not in Name=value form", s)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return AttrSpec{}, errors.New("radius: attribute has no name")
	}

	spec := AttrSpec{Value: value, Name: name}

	if def := d.ByName(name); def != nil {
		spec.Vendor, spec.Type, spec.Def = def.Vendor, def.Type, def
		return spec, nil
	}

	parts := strings.Split(name, ".")
	// "26.<vendor>.<type>" is the same thing as "<vendor>.<type>"; accept
	// both because both appear in the wild.
	if len(parts) == 3 && parts[0] == strconv.Itoa(int(AttrVendorSpecific)) {
		parts = parts[1:]
	}

	switch len(parts) {
	case 1:
		n, err := strconv.ParseUint(parts[0], 10, 8)
		if err != nil {
			return AttrSpec{}, fmt.Errorf("radius: unknown attribute %q; load a dictionary or write it by number", name)
		}
		spec.Type = AttrType(n)
		spec.Def = d.ByNumber(spec.Type)

	case 2:
		vendor, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			if id, ok := d.VendorID(parts[0]); ok {
				vendor = uint64(id)
			} else {
				return AttrSpec{}, fmt.Errorf("radius: unknown vendor %q in %q", parts[0], name)
			}
		}
		t, err := strconv.ParseUint(parts[1], 10, 8)
		if err != nil {
			return AttrSpec{}, fmt.Errorf("radius: vendor attribute number in %q: %w", name, err)
		}
		spec.Vendor, spec.Type = uint32(vendor), AttrType(t)
		spec.Def = d.ByVendor(spec.Vendor, spec.Type)

	default:
		return AttrSpec{}, fmt.Errorf("radius: cannot interpret attribute name %q", name)
	}

	return spec, nil
}

// EncodeValue turns a rendered value into its wire form for this attribute.
//
// An attribute with no definition is carried as text, which is what a caller
// writing a raw number almost always means. Binary values can be given as
// "0x" followed by hexadecimal whatever the declared type.
func (s AttrSpec) EncodeValue(rendered string) ([]byte, error) {
	if strings.HasPrefix(rendered, "0x") || strings.HasPrefix(rendered, "0X") {
		b, err := hex.DecodeString(rendered[2:])
		if err != nil {
			return nil, fmt.Errorf("radius: %s: hexadecimal value: %w", s.Name, err)
		}
		return b, nil
	}

	data := TypeString
	if s.Def != nil {
		data = s.Def.Data
	}

	switch data {
	case TypeString, TypeOctets:
		return []byte(rendered), nil

	case TypeInteger, TypeByte, TypeShort:
		n, err := s.parseInteger(rendered)
		if err != nil {
			return nil, err
		}
		switch data {
		case TypeByte:
			if n > 0xff {
				return nil, fmt.Errorf("radius: %s: %d does not fit in one octet", s.Name, n)
			}
			return []byte{byte(n)}, nil
		case TypeShort:
			if n > 0xffff {
				return nil, fmt.Errorf("radius: %s: %d does not fit in two octets", s.Name, n)
			}
			var b [2]byte
			binary.BigEndian.PutUint16(b[:], uint16(n))
			return b[:], nil
		default:
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], n)
			return b[:], nil
		}

	case TypeDate:
		if n, err := strconv.ParseUint(rendered, 10, 32); err == nil {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], uint32(n))
			return b[:], nil
		}
		t, err := time.Parse(time.RFC3339, rendered)
		if err != nil {
			return nil, fmt.Errorf("radius: %s: expected seconds since the epoch or an RFC 3339 time", s.Name)
		}
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(t.Unix()))
		return b[:], nil

	case TypeIPAddr:
		a, err := netip.ParseAddr(rendered)
		if err != nil || !a.Is4() {
			return nil, fmt.Errorf("radius: %s: expected an IPv4 address, got %q", s.Name, rendered)
		}
		b := a.As4()
		return b[:], nil

	case TypeIPv6Addr:
		a, err := netip.ParseAddr(rendered)
		if err != nil || !a.Is6() {
			return nil, fmt.Errorf("radius: %s: expected an IPv6 address, got %q", s.Name, rendered)
		}
		b := a.As16()
		return b[:], nil

	case TypeIPv6Prefix:
		pfx, err := netip.ParsePrefix(rendered)
		if err != nil || !pfx.Addr().Is6() {
			return nil, fmt.Errorf("radius: %s: expected an IPv6 prefix, got %q", s.Name, rendered)
		}
		addr := pfx.Addr().As16()
		// A reserved octet, the prefix length, then the prefix truncated to
		// whole octets (RFC 3162 section 2.3).
		n := (pfx.Bits() + 7) / 8
		out := make([]byte, 2+n)
		out[1] = byte(pfx.Bits())
		copy(out[2:], addr[:n])
		return out, nil

	default:
		return []byte(rendered), nil
	}
}

// parseInteger accepts a number or a name the dictionary defines for this
// attribute, so Service-Type=Framed-User works as readily as Service-Type=2.
func (s AttrSpec) parseInteger(rendered string) (uint32, error) {
	if n, err := strconv.ParseUint(rendered, 10, 32); err == nil {
		return uint32(n), nil
	}
	if s.Def != nil {
		if n, ok := s.Def.ValueByName(rendered); ok {
			return n, nil
		}
	}
	return 0, fmt.Errorf("radius: %s: %q is not a number or a known value name", s.Name, rendered)
}
