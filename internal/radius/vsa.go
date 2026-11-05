package radius

import (
	"encoding/binary"
	"fmt"
)

// AttrVendorSpecific is the standard attribute that carries vendor attributes
// (RFC 2865 section 5.26).
const AttrVendorSpecific AttrType = 26

// vsaHeaderLen is the fixed part of a Vendor-Specific attribute: the two octet
// attribute header plus the four octet vendor identifier.
const vsaHeaderLen = 6

// maxVSAValueLen is the largest value a vendor attribute can carry in the
// common encoding, being 255 less the attribute header, the vendor identifier
// and the vendor type and length octets.
const maxVSAValueLen = 255 - vsaHeaderLen - 2

// appendVSA writes a vendor attribute in the encoding almost every vendor
// uses: a one octet vendor type followed by a one octet length.
//
// The RFC allows a vendor to choose other widths, and a handful do, but they
// are rare enough that supporting them on the sending side would add a
// dictionary lookup to the hot path for no practical gain. A vendor needing a
// different layout can still be expressed by building the enclosing attribute
// 26 value directly.
func appendVSA(dst []byte, a Attribute) ([]byte, error) {
	if len(a.Value) > maxVSAValueLen {
		return nil, fmt.Errorf("%w: vendor %d attribute %d is %d octets, over the %d the encoding allows",
			ErrValueTooLong, a.Vendor, a.Type, len(a.Value), maxVSAValueLen)
	}

	total := vsaHeaderLen + 2 + len(a.Value)
	dst = append(dst, byte(AttrVendorSpecific), byte(total))

	var vid [4]byte
	binary.BigEndian.PutUint32(vid[:], a.Vendor)
	dst = append(dst, vid[:]...)

	dst = append(dst, byte(a.Type), byte(len(a.Value)+2))
	return append(dst, a.Value...), nil
}

// appendVendorAttributes parses the body of a Vendor-Specific attribute and
// appends what it finds.
//
// One Vendor-Specific attribute may carry several vendor attributes back to
// back. A body that does not parse cleanly is kept whole as an attribute 26
// with no vendor rather than being dropped, so a packet using a layout this
// does not understand still round-trips and is still visible to a reader.
func appendVendorAttributes(out []Attribute, body []byte) []Attribute {
	if len(body) < 5 {
		return append(out, Attribute{Type: AttrVendorSpecific, Value: body})
	}
	vendor := binary.BigEndian.Uint32(body[:4])
	if vendor == 0 {
		return append(out, Attribute{Type: AttrVendorSpecific, Value: body})
	}

	// Walk the vendor TLVs, checking the whole run before keeping any of it.
	rest := body[4:]
	var parsed []Attribute
	for len(rest) > 0 {
		if len(rest) < 2 {
			return append(out, Attribute{Type: AttrVendorSpecific, Value: body})
		}
		vlen := int(rest[1])
		if vlen < 2 || vlen > len(rest) {
			return append(out, Attribute{Type: AttrVendorSpecific, Value: body})
		}
		parsed = append(parsed, Attribute{
			Type:   AttrType(rest[0]),
			Vendor: vendor,
			Value:  rest[2:vlen],
		})
		rest = rest[vlen:]
	}
	return append(out, parsed...)
}
