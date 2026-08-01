package radius

import (
	"slices"
	"strings"
)

// AttrType is a RADIUS attribute number.
type AttrType uint8

// Standard attributes used when building broadband access requests and
// accounting records. Numbers are the IANA assignments from RFC 2865,
// RFC 2866, RFC 2869, RFC 3162, RFC 4818, RFC 5176 and RFC 6911.
const (
	AttrUserName             AttrType = 1
	AttrUserPassword         AttrType = 2
	AttrCHAPPassword         AttrType = 3
	AttrNASIPAddress         AttrType = 4
	AttrNASPort              AttrType = 5
	AttrServiceType          AttrType = 6
	AttrFramedProtocol       AttrType = 7
	AttrFramedIPAddress      AttrType = 8
	AttrFramedIPNetmask      AttrType = 9
	AttrFilterID             AttrType = 11
	AttrReplyMessage         AttrType = 18
	AttrFramedRoute          AttrType = 22
	AttrClass                AttrType = 25
	AttrSessionTimeout       AttrType = 27
	AttrIdleTimeout          AttrType = 28
	AttrCalledStationID      AttrType = 30
	AttrCallingStationID     AttrType = 31
	AttrNASIdentifier        AttrType = 32
	AttrProxyState           AttrType = 33
	AttrAcctStatusType       AttrType = 40
	AttrAcctDelayTime        AttrType = 41
	AttrAcctInputOctets      AttrType = 42
	AttrAcctOutputOctets     AttrType = 43
	AttrAcctSessionID        AttrType = 44
	AttrAcctAuthentic        AttrType = 45
	AttrAcctSessionTime      AttrType = 46
	AttrAcctInputPackets     AttrType = 47
	AttrAcctOutputPackets    AttrType = 48
	AttrAcctTerminateCause   AttrType = 49
	AttrAcctMultiSessionID   AttrType = 50
	AttrAcctLinkCount        AttrType = 51
	AttrAcctInputGigawords   AttrType = 52
	AttrAcctOutputGigawords  AttrType = 53
	AttrEventTimestamp       AttrType = 55
	AttrCHAPChallenge        AttrType = 60
	AttrNASPortType          AttrType = 61
	AttrAcctInterimInterval  AttrType = 85
	AttrNASPortID            AttrType = 87
	AttrFramedPool           AttrType = 88
	AttrNASIPv6Address       AttrType = 95
	AttrFramedIPv6Prefix     AttrType = 97
	AttrFramedIPv6Pool       AttrType = 100
	AttrErrorCause           AttrType = 101
	AttrDelegatedIPv6Prefix  AttrType = 123
	AttrMessageAuthenticator AttrType = 80
	AttrFramedIPv6Address    AttrType = 168
)

// DataType describes how an attribute value is carried on the wire.
type DataType uint8

// Wire representations.
const (
	TypeString     DataType = iota // UTF-8 text, no trailing NUL
	TypeOctets                     // opaque binary
	TypeIPAddr                     // 4 octets
	TypeIPv6Addr                   // 16 octets
	TypeIPv6Prefix                 // reserved octet, prefix length, prefix
	TypeInteger                    // 32-bit unsigned, network byte order
	TypeDate                       // 32-bit unsigned, seconds since the Unix epoch
	TypeByte                       // 8-bit unsigned
	TypeShort                      // 16-bit unsigned, network byte order
)

// Encryption describes the obfuscation applied to a value before transmission.
type Encryption uint8

// Encryption schemes.
const (
	EncryptionNone         Encryption = iota
	EncryptionUserPassword            // RFC 2865 section 5.2
)

// Value is a named constant for an integer attribute.
type Value struct {
	Name   string
	Number uint32
}

// AttrDef describes one attribute.
type AttrDef struct {
	Type AttrType
	// Vendor is zero for a standard attribute and the vendor's identifier
	// for one carried inside a Vendor-Specific attribute.
	Vendor uint32
	// Name is the name as the dictionary file wrote it, which for a vendor
	// attribute may be bare. Qualified is the name that actually resolves,
	// and is what an interface must show: offering a bare name would suggest
	// something that does not parse, and two vendors' identically named
	// attributes would be indistinguishable.
	Name      string
	Qualified string
	Data      DataType
	Encrypt   Encryption
	HasTag    bool
	Values    []Value
}

// standardAttrs is the built-in attribute set. It is deliberately limited to
// attributes defined by the RFCs; vendor-specific attributes are layered on
// top by loading additional definitions at runtime.
var standardAttrs = []AttrDef{
	{Type: AttrUserName, Name: "User-Name", Data: TypeString},
	{Type: AttrUserPassword, Name: "User-Password", Data: TypeString, Encrypt: EncryptionUserPassword},
	{Type: AttrCHAPPassword, Name: "CHAP-Password", Data: TypeOctets},
	{Type: AttrNASIPAddress, Name: "NAS-IP-Address", Data: TypeIPAddr},
	{Type: AttrNASPort, Name: "NAS-Port", Data: TypeInteger},
	{Type: AttrServiceType, Name: "Service-Type", Data: TypeInteger, Values: []Value{
		{"Login-User", 1}, {"Framed-User", 2}, {"Callback-Login-User", 3},
		{"Callback-Framed-User", 4}, {"Outbound-User", 5}, {"Administrative-User", 6},
		{"NAS-Prompt-User", 7}, {"Authenticate-Only", 8}, {"Callback-NAS-Prompt", 9},
		{"Call-Check", 10}, {"Callback-Administrative", 11},
	}},
	{Type: AttrFramedProtocol, Name: "Framed-Protocol", Data: TypeInteger, Values: []Value{
		{"PPP", 1}, {"SLIP", 2}, {"ARAP", 3}, {"Gandalf-SLML", 4},
		{"Xylogics-IPX-SLIP", 5}, {"X.75-Synchronous", 6},
	}},
	{Type: AttrFramedIPAddress, Name: "Framed-IP-Address", Data: TypeIPAddr},
	{Type: AttrFramedIPNetmask, Name: "Framed-IP-Netmask", Data: TypeIPAddr},
	{Type: AttrFilterID, Name: "Filter-Id", Data: TypeString},
	{Type: AttrReplyMessage, Name: "Reply-Message", Data: TypeString},
	{Type: AttrFramedRoute, Name: "Framed-Route", Data: TypeString},
	{Type: AttrClass, Name: "Class", Data: TypeOctets},
	{Type: AttrSessionTimeout, Name: "Session-Timeout", Data: TypeInteger},
	{Type: AttrIdleTimeout, Name: "Idle-Timeout", Data: TypeInteger},
	{Type: AttrCalledStationID, Name: "Called-Station-Id", Data: TypeString},
	{Type: AttrCallingStationID, Name: "Calling-Station-Id", Data: TypeString},
	{Type: AttrNASIdentifier, Name: "NAS-Identifier", Data: TypeString},
	{Type: AttrProxyState, Name: "Proxy-State", Data: TypeOctets},
	{Type: AttrAcctStatusType, Name: "Acct-Status-Type", Data: TypeInteger, Values: []Value{
		{"Start", 1}, {"Stop", 2}, {"Interim-Update", 3},
		{"Accounting-On", 7}, {"Accounting-Off", 8},
	}},
	{Type: AttrAcctDelayTime, Name: "Acct-Delay-Time", Data: TypeInteger},
	{Type: AttrAcctInputOctets, Name: "Acct-Input-Octets", Data: TypeInteger},
	{Type: AttrAcctOutputOctets, Name: "Acct-Output-Octets", Data: TypeInteger},
	{Type: AttrAcctSessionID, Name: "Acct-Session-Id", Data: TypeString},
	{Type: AttrAcctAuthentic, Name: "Acct-Authentic", Data: TypeInteger, Values: []Value{
		{"RADIUS", 1}, {"Local", 2}, {"Remote", 3},
	}},
	{Type: AttrAcctSessionTime, Name: "Acct-Session-Time", Data: TypeInteger},
	{Type: AttrAcctInputPackets, Name: "Acct-Input-Packets", Data: TypeInteger},
	{Type: AttrAcctOutputPackets, Name: "Acct-Output-Packets", Data: TypeInteger},
	{Type: AttrAcctTerminateCause, Name: "Acct-Terminate-Cause", Data: TypeInteger, Values: []Value{
		{"User-Request", 1}, {"Lost-Carrier", 2}, {"Lost-Service", 3},
		{"Idle-Timeout", 4}, {"Session-Timeout", 5}, {"Admin-Reset", 6},
		{"Admin-Reboot", 7}, {"Port-Error", 8}, {"NAS-Error", 9},
		{"NAS-Request", 10}, {"NAS-Reboot", 11}, {"Port-Unneeded", 12},
		{"Port-Preempted", 13}, {"Port-Suspended", 14}, {"Service-Unavailable", 15},
		{"Callback", 16}, {"User-Error", 17}, {"Host-Request", 18},
	}},
	{Type: AttrAcctMultiSessionID, Name: "Acct-Multi-Session-Id", Data: TypeString},
	{Type: AttrAcctLinkCount, Name: "Acct-Link-Count", Data: TypeInteger},
	{Type: AttrAcctInputGigawords, Name: "Acct-Input-Gigawords", Data: TypeInteger},
	{Type: AttrAcctOutputGigawords, Name: "Acct-Output-Gigawords", Data: TypeInteger},
	{Type: AttrEventTimestamp, Name: "Event-Timestamp", Data: TypeDate},
	{Type: AttrCHAPChallenge, Name: "CHAP-Challenge", Data: TypeOctets},
	{Type: AttrNASPortType, Name: "NAS-Port-Type", Data: TypeInteger, Values: []Value{
		{"Async", 0}, {"Sync", 1}, {"ISDN", 2}, {"ISDN-V120", 3}, {"ISDN-V110", 4},
		{"Virtual", 5}, {"PIAFS", 6}, {"HDLC-Clear-Channel", 7}, {"X.25", 8},
		{"X.75", 9}, {"G.3-Fax", 10}, {"SDSL", 11}, {"ADSL-CAP", 12},
		{"ADSL-DMT", 13}, {"IDSL", 14}, {"Ethernet", 15}, {"xDSL", 16},
		{"Cable", 17}, {"Wireless-Other", 18}, {"Wireless-802.11", 19},
	}},
	{Type: AttrMessageAuthenticator, Name: "Message-Authenticator", Data: TypeOctets},
	{Type: AttrAcctInterimInterval, Name: "Acct-Interim-Interval", Data: TypeInteger},
	{Type: AttrNASPortID, Name: "NAS-Port-Id", Data: TypeString},
	{Type: AttrFramedPool, Name: "Framed-Pool", Data: TypeString},
	{Type: AttrNASIPv6Address, Name: "NAS-IPv6-Address", Data: TypeIPv6Addr},
	{Type: AttrFramedIPv6Prefix, Name: "Framed-IPv6-Prefix", Data: TypeIPv6Prefix},
	{Type: AttrFramedIPv6Pool, Name: "Framed-IPv6-Pool", Data: TypeString},
	{Type: AttrErrorCause, Name: "Error-Cause", Data: TypeInteger, Values: []Value{
		{"Residual-Session-Context-Removed", 201}, {"Invalid-EAP-Packet", 202},
		{"Unsupported-Attribute", 401}, {"Missing-Attribute", 402},
		{"NAS-Identification-Mismatch", 403}, {"Invalid-Request", 404},
		{"Unsupported-Service", 405}, {"Unsupported-Extension", 406},
		{"Invalid-Attribute-Value", 407}, {"Administratively-Prohibited", 501},
		{"Request-Not-Routable", 502}, {"Session-Context-Not-Found", 503},
		{"Session-Context-Not-Removable", 504}, {"Other-Proxy-Processing-Error", 505},
		{"Resources-Unavailable", 506}, {"Request-Initiated", 507},
	}},
	{Type: AttrDelegatedIPv6Prefix, Name: "Delegated-IPv6-Prefix", Data: TypeIPv6Prefix},
	{Type: AttrFramedIPv6Address, Name: "Framed-IPv6-Address", Data: TypeIPv6Addr},
}

// Dictionary resolves attribute names and numbers.
//
// The built-in set covers the RFC attributes only. Vendor attributes come from
// dictionary files the operator already has, loaded at runtime, so both ends
// of a test resolve the same names to the same numbers.
type Dictionary struct {
	byNumber   [256]*AttrDef
	byName     map[string]*AttrDef
	vendorIDs  map[string]uint32
	vendorName map[uint32]string
	byVendor   map[uint32]map[AttrType]*AttrDef
}

// StandardDictionary returns a dictionary containing the built-in RFC
// attribute set. The returned value is shared and must not be mutated.
func StandardDictionary() *Dictionary { return stdDict }

var stdDict = newStandardDictionary()

func newStandardDictionary() *Dictionary {
	d := &Dictionary{
		byName:     make(map[string]*AttrDef, len(standardAttrs)),
		vendorIDs:  make(map[string]uint32),
		vendorName: make(map[uint32]string),
		byVendor:   make(map[uint32]map[AttrType]*AttrDef),
	}
	for i := range standardAttrs {
		def := &standardAttrs[i]
		def.Qualified = def.Name
		d.byNumber[def.Type] = def
		d.byName[strings.ToLower(def.Name)] = def
	}
	return d
}

// ByNumber returns the definition for t, or nil if t is unknown.
func (d *Dictionary) ByNumber(t AttrType) *AttrDef { return d.byNumber[t] }

// ByName returns the definition named n, matched case-insensitively, or nil if
// n is unknown.
func (d *Dictionary) ByName(n string) *AttrDef { return d.byName[strings.ToLower(n)] }

// ValueByName returns the numeric constant named v for this attribute.
func (a *AttrDef) ValueByName(v string) (uint32, bool) {
	for _, val := range a.Values {
		if strings.EqualFold(val.Name, v) {
			return val.Number, true
		}
	}
	return 0, false
}

// ValueName returns the name of constant n, or the empty string if n has no
// name in this attribute's value list.
func (a *AttrDef) ValueName(n uint32) string {
	for _, val := range a.Values {
		if val.Number == n {
			return val.Name
		}
	}
	return ""
}

// Match is one result of searching the dictionary.
type Match struct {
	Name   string   `json:"name"`
	Vendor uint32   `json:"vendor,omitempty"`
	Type   AttrType `json:"type"`
	// Data is the declared wire type, so an interface can hint at what a
	// value should look like.
	Data DataType `json:"data"`
	// Values are the named constants for an integer attribute, which are what
	// an operator actually wants to pick from.
	Values []string `json:"values,omitempty"`
}

// Search finds attributes whose name contains q, case-insensitively.
//
// Names that start with q come first, because someone typing "cisco" wants the
// Cisco attributes before every attribute that merely mentions it.
func (d *Dictionary) Search(q string, limit int) []Match {
	if limit <= 0 {
		limit = 50
	}
	q = strings.ToLower(strings.TrimSpace(q))

	var prefix, contains []*AttrDef
	seen := make(map[*AttrDef]bool)
	for name, def := range d.byName {
		if seen[def] {
			continue
		}
		switch {
		case q == "":
			seen[def] = true
			prefix = append(prefix, def)
		case strings.HasPrefix(name, q):
			seen[def] = true
			prefix = append(prefix, def)
		case strings.Contains(name, q):
			seen[def] = true
			contains = append(contains, def)
		}
		if len(prefix) > limit*4 {
			break
		}
	}

	byName := func(a, b *AttrDef) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(prefix, byName)
	slices.SortFunc(contains, byName)

	out := make([]Match, 0, limit)
	for _, def := range append(prefix, contains...) {
		if len(out) >= limit {
			break
		}
		name := def.Qualified
		if name == "" {
			name = def.Name
		}
		m := Match{Name: name, Vendor: def.Vendor, Type: def.Type, Data: def.Data}
		for _, v := range def.Values {
			m.Values = append(m.Values, v.Name)
		}
		out = append(out, m)
	}
	return out
}

// Count returns how many attributes and vendors the dictionary knows, which is
// what an interface reports after loading.
func (d *Dictionary) Count() (attrs, vendors int) {
	seen := make(map[*AttrDef]bool)
	for _, def := range d.byName {
		seen[def] = true
	}
	return len(seen), len(d.vendorIDs)
}
