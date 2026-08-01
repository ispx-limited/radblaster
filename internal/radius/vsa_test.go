package radius

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
)

const exampleVendor = 12345

func TestVendorAttributeRoundTrip(t *testing.T) {
	p := &Packet{Code: CodeAccessRequest, Identifier: 1}
	p.AddString(AttrUserName, "subscriber@example.net")
	p.AddVendorString(exampleVendor, 1, "ip:qos-policy-in=voice")
	p.AddVendor(exampleVendor, 2, []byte{0, 0, 0, 42})

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if v := string(got.GetVendor(exampleVendor, 1)); v != "ip:qos-policy-in=voice" {
		t.Errorf("vendor attribute 1 = %q, want the AV pair", v)
	}
	if v := got.GetVendor(exampleVendor, 2); !bytes.Equal(v, []byte{0, 0, 0, 42}) {
		t.Errorf("vendor attribute 2 = %v, want 0 0 0 42", v)
	}
	// A vendor attribute must not be reachable as a standard one, or a
	// vendor number colliding with a standard number would silently shadow it.
	if v := got.Get(1); string(v) != "subscriber@example.net" {
		t.Errorf("standard attribute 1 = %q, want the username; vendor attributes must not shadow it", v)
	}
}

// TestVendorAttributeWireLayout pins the encoding a peer expects: attribute 26
// carrying the vendor identifier, then a one octet type and length.
func TestVendorAttributeWireLayout(t *testing.T) {
	p := &Packet{Code: CodeAccountingRequest, Identifier: 1}
	p.AddVendorString(exampleVendor, 7, "abc")

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	body := b[HeaderLen:]
	if AttrType(body[0]) != AttrVendorSpecific {
		t.Fatalf("attribute type = %d, want %d (Vendor-Specific)", body[0], AttrVendorSpecific)
	}
	if body[1] != 6+2+3 {
		t.Errorf("attribute length = %d, want %d", body[1], 6+2+3)
	}
	if v := binary.BigEndian.Uint32(body[2:6]); v != exampleVendor {
		t.Errorf("vendor id = %d, want %d", v, exampleVendor)
	}
	if body[6] != 7 {
		t.Errorf("vendor type = %d, want 7", body[6])
	}
	if body[7] != 2+3 {
		t.Errorf("vendor length = %d, want 5", body[7])
	}
	if string(body[8:11]) != "abc" {
		t.Errorf("value = %q, want abc", body[8:11])
	}
}

// TestSeveralVendorAttributesInOneContainer covers the packing a real server
// may use, where one Vendor-Specific attribute carries several vendor TLVs.
func TestSeveralVendorAttributesInOneContainer(t *testing.T) {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, exampleVendor)
	body = append(body, 1, 2+3, 'o', 'n', 'e')
	body = append(body, 2, 2+3, 't', 'w', 'o')

	pkt := []byte{byte(CodeAccessAccept), 1, 0, 0}
	pkt = append(pkt, make([]byte, AuthenticatorLen)...)
	pkt = append(pkt, byte(AttrVendorSpecific), byte(2+len(body)))
	pkt = append(pkt, body...)
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))

	got, err := Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got.Attributes) != 2 {
		t.Fatalf("decoded %d attributes, want 2", len(got.Attributes))
	}
	if v := string(got.GetVendor(exampleVendor, 1)); v != "one" {
		t.Errorf("vendor attribute 1 = %q, want one", v)
	}
	if v := string(got.GetVendor(exampleVendor, 2)); v != "two" {
		t.Errorf("vendor attribute 2 = %q, want two", v)
	}
}

// TestMalformedVendorBodyIsKeptWhole matters because a packet using a layout
// this does not model must still round-trip and stay visible, rather than
// having attributes silently vanish.
func TestMalformedVendorBodyIsKeptWhole(t *testing.T) {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, exampleVendor)
	body = append(body, 1, 200) // a length running past the end

	pkt := []byte{byte(CodeAccessAccept), 1, 0, 0}
	pkt = append(pkt, make([]byte, AuthenticatorLen)...)
	pkt = append(pkt, byte(AttrVendorSpecific), byte(2+len(body)))
	pkt = append(pkt, body...)
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))

	got, err := Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got.Attributes) != 1 {
		t.Fatalf("decoded %d attributes, want 1", len(got.Attributes))
	}
	a := got.Attributes[0]
	if a.Type != AttrVendorSpecific || a.Vendor != 0 {
		t.Errorf("attribute = type %d vendor %d, want the raw Vendor-Specific kept whole", a.Type, a.Vendor)
	}
}

func TestVendorValueTooLongIsRejected(t *testing.T) {
	p := &Packet{Code: CodeAccessRequest, Identifier: 1}
	p.AddVendor(exampleVendor, 1, make([]byte, maxVSAValueLen+1))

	if _, err := p.Encode(nil, testSecret); !errors.Is(err, ErrValueTooLong) {
		t.Errorf("err = %v, want ErrValueTooLong", err)
	}
}

func loadTestDict(t *testing.T) *Dictionary {
	t.Helper()
	d, err := LoadDictionary(filepath.Join("testdata", "dict", "dictionary"))
	if err != nil {
		t.Fatalf("LoadDictionary: %v", err)
	}
	return d
}

func TestLoadDictionary(t *testing.T) {
	d := loadTestDict(t)

	// The built-in RFC set must survive loading.
	if def := d.ByName("User-Name"); def == nil || def.Type != AttrUserName {
		t.Error("the standard attributes were lost when a dictionary was loaded")
	}

	def := d.ByName("Example-AVPair")
	if def == nil {
		t.Fatal("Example-AVPair was not loaded")
	}
	if def.Vendor != exampleVendor || def.Type != 1 || def.Data != TypeString {
		t.Errorf("Example-AVPair = vendor %d type %d data %d, want %d/1/string",
			def.Vendor, def.Type, def.Data, exampleVendor)
	}

	// $INCLUDE must have pulled in the second file, and hexadecimal vendor
	// numbers must parse.
	if id, ok := d.VendorID("Acme"); !ok || id != 0x1234 {
		t.Errorf("Acme vendor id = %d/%v, want 4660", id, ok)
	}
	if def := d.ByName("Site-Local-Tag"); def == nil || def.Type != 200 {
		t.Error("a standard attribute added by a loaded dictionary was not registered")
	}
}

// TestLoadDictionarySkipsUnsupportedConstructs keeps one unfamiliar line from
// making a whole real-world dictionary file unusable.
func TestLoadDictionarySkipsUnsupportedConstructs(t *testing.T) {
	d := loadTestDict(t)

	if d.ByName("Acme-Nested") != nil {
		t.Error("an attribute with a nested number was loaded; it should have been skipped")
	}
	// The rest of the file must still be there.
	if d.ByName("Acme-Circuit-Id") == nil {
		t.Error("a later attribute was lost after an unsupported line")
	}
}

func TestDictionaryValueNames(t *testing.T) {
	d := loadTestDict(t)

	def := d.ByName("Example-Policy")
	if def == nil {
		t.Fatal("Example-Policy was not loaded")
	}
	n, ok := def.ValueByName("Silver")
	if !ok || n != 2 {
		t.Errorf("Silver = %d/%v, want 2/true", n, ok)
	}
	if got := def.ValueName(3); got != "Bronze" {
		t.Errorf("value 3 = %q, want Bronze", got)
	}
}

// TestStandardDictionaryIsNotMutated guards the shared built-in set against
// being changed by a load.
func TestStandardDictionaryIsNotMutated(t *testing.T) {
	loadTestDict(t)

	if StandardDictionary().ByName("Example-AVPair") != nil {
		t.Error("loading a dictionary leaked definitions into the shared standard set")
	}
	if _, ok := StandardDictionary().VendorID("Example"); ok {
		t.Error("loading a dictionary leaked a vendor into the shared standard set")
	}
}

func TestParseSpec(t *testing.T) {
	d := loadTestDict(t)

	tests := []struct {
		spec       string
		wantVendor uint32
		wantType   AttrType
	}{
		{"User-Name=alice", 0, AttrUserName},
		{"Example-AVPair=ip:qos=voice", exampleVendor, 1},
		{"32=my-nas", 0, AttrNASIdentifier},
		{"12345.1=ip:qos=voice", exampleVendor, 1},
		{"26.12345.1=ip:qos=voice", exampleVendor, 1},
		{"Example.2=100", exampleVendor, 2},
	}

	for _, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := d.ParseSpec(tc.spec)
			if err != nil {
				t.Fatalf("ParseSpec: %v", err)
			}
			if got.Vendor != tc.wantVendor || got.Type != tc.wantType {
				t.Errorf("= vendor %d type %d, want vendor %d type %d",
					got.Vendor, got.Type, tc.wantVendor, tc.wantType)
			}
		})
	}
}

func TestParseSpecRejectsNonsense(t *testing.T) {
	d := loadTestDict(t)

	for _, spec := range []string{
		"no-equals-sign",
		"=value",
		"Not-A-Known-Attribute=x",
		"Unknown-Vendor-Name.1=x",
		"12345.not-a-number=x",
		"a.b.c.d=x",
	} {
		t.Run(spec, func(t *testing.T) {
			if _, err := d.ParseSpec(spec); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// TestParseSpecAcceptsUnknownVendorNumbers is the property that keeps the tool
// usable without any dictionary at all: a vendor nobody has declared can still
// be addressed by number.
func TestParseSpecAcceptsUnknownVendorNumbers(t *testing.T) {
	d := StandardDictionary()

	got, err := d.ParseSpec("9999.7=ip:qos=voice")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if got.Vendor != 9999 || got.Type != 7 {
		t.Errorf("= vendor %d type %d, want 9999/7", got.Vendor, got.Type)
	}
	if got.Def != nil {
		t.Error("an undeclared vendor attribute should have no definition")
	}
	// With no definition the value is carried as text, which is what someone
	// writing a raw vendor attribute almost always means.
	v, err := got.EncodeValue(got.Value)
	if err != nil {
		t.Fatalf("EncodeValue: %v", err)
	}
	if string(v) != "ip:qos=voice" {
		t.Errorf("value = %q, want the text as written", v)
	}
}

func TestEncodeValue(t *testing.T) {
	d := loadTestDict(t)

	tests := []struct {
		spec string
		want []byte
	}{
		{"User-Name=alice", []byte("alice")},
		{"Example-Bandwidth-Up=1000", []byte{0, 0, 0x03, 0xe8}},
		{"Example-Gateway=192.0.2.1", []byte{192, 0, 2, 1}},
		{"Example-Flags=7", []byte{7}},
		{"Example-Policy=Silver", []byte{0, 0, 0, 2}},
		{"Service-Type=Framed-User", []byte{0, 0, 0, 2}},
		{"Class=0xdeadbeef", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"Delegated-IPv6-Prefix=2001:db8::/32", []byte{0, 32, 0x20, 0x01, 0x0d, 0xb8}},
	}

	for _, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			spec, err := d.ParseSpec(tc.spec)
			if err != nil {
				t.Fatalf("ParseSpec: %v", err)
			}
			got, err := spec.EncodeValue(spec.Value)
			if err != nil {
				t.Fatalf("EncodeValue: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("= %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEncodeValueRejectsBadInput(t *testing.T) {
	d := loadTestDict(t)

	for _, spec := range []string{
		"Example-Bandwidth-Up=not-a-number",
		"Example-Gateway=not-an-address",
		"Example-Flags=999",
		"Example-Policy=Platinum",
	} {
		t.Run(spec, func(t *testing.T) {
			s, err := d.ParseSpec(spec)
			if err != nil {
				t.Fatalf("ParseSpec: %v", err)
			}
			if _, err := s.EncodeValue(s.Value); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// TestVendorAttributeNaming checks the display path a packet inspector uses.
func TestVendorAttributeNaming(t *testing.T) {
	d := loadTestDict(t)

	if got := d.VendorAttrName(exampleVendor, 1); got != "Example-AVPair" {
		t.Errorf("name = %q, want Example-AVPair", got)
	}
	if got := d.FormatVendorValue(exampleVendor, 5, []byte{0, 0, 0, 1}); got != "Gold (1)" {
		t.Errorf("value = %q, want Gold (1)", got)
	}
	// An attribute of a vendor the dictionary knows, but not this number.
	if got := d.VendorAttrName(exampleVendor, 99); got != "Example-99" {
		t.Errorf("name = %q, want Example-99", got)
	}
	// A vendor the dictionary has never heard of.
	if got := d.VendorAttrName(4242, 1); got != "Vendor-4242-1" {
		t.Errorf("name = %q, want Vendor-4242-1", got)
	}
}
