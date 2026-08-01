package radius

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"time"
)

// AttrName returns a human-readable name for t, falling back to the bare
// number when the dictionary does not define it.
func (d *Dictionary) AttrName(t AttrType) string {
	if def := d.ByNumber(t); def != nil {
		return def.Name
	}
	return "Unknown-" + strconv.Itoa(int(t))
}

// FormatValue renders v for display according to the attribute's data type.
// Values that do not match the declared width fall back to hex, so a malformed
// packet is still readable rather than being hidden behind an error.
func (d *Dictionary) FormatValue(t AttrType, v []byte) string {
	def := d.ByNumber(t)
	if def == nil {
		return fmt.Sprintf("%#x", v)
	}

	switch def.Data {
	case TypeString:
		return strconv.Quote(string(v))

	case TypeInteger:
		if len(v) != 4 {
			break
		}
		n := binary.BigEndian.Uint32(v)
		if name := def.ValueName(n); name != "" {
			return fmt.Sprintf("%s (%d)", name, n)
		}
		return strconv.FormatUint(uint64(n), 10)

	case TypeDate:
		if len(v) != 4 {
			break
		}
		n := binary.BigEndian.Uint32(v)
		return time.Unix(int64(n), 0).UTC().Format(time.RFC3339)

	case TypeIPAddr:
		if len(v) != 4 {
			break
		}
		a, _ := netip.AddrFromSlice(v)
		return a.String()

	case TypeIPv6Addr:
		if len(v) != 16 {
			break
		}
		a, _ := netip.AddrFromSlice(v)
		return a.String()

	case TypeIPv6Prefix:
		// One reserved octet, one prefix-length octet, then the prefix
		// truncated to whole octets (RFC 3162 section 2.3).
		if len(v) < 2 || v[1] > 128 {
			break
		}
		var full [16]byte
		copy(full[:], v[2:])
		a := netip.AddrFrom16(full)
		return fmt.Sprintf("%s/%d", a.String(), v[1])
	}

	return fmt.Sprintf("%#x", v)
}
