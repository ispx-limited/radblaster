package radius

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
)

// Wire limits from RFC 2865 section 3.
const (
	// HeaderLen is the fixed size of the RADIUS header: code, identifier,
	// length and authenticator.
	HeaderLen = 20
	// AuthenticatorLen is the size of the authenticator field.
	AuthenticatorLen = 16
	// MaxPacketLen is the largest packet the protocol permits.
	MaxPacketLen = 4096
	// MaxValueLen is the largest value a single attribute can carry, being
	// 255 less the two octet attribute header.
	MaxValueLen = 253

	// messageAuthenticatorLen is the total encoded size of a
	// Message-Authenticator attribute: two octet header plus a 16 octet
	// HMAC-MD5 digest.
	messageAuthenticatorLen = 18
)

// Errors returned when parsing or building packets.
var (
	ErrShortPacket    = errors.New("radius: packet shorter than the header")
	ErrBadLength      = errors.New("radius: declared length outside the legal range")
	ErrTruncated      = errors.New("radius: packet shorter than its declared length")
	ErrBadAttribute   = errors.New("radius: malformed attribute")
	ErrValueTooLong   = errors.New("radius: attribute value exceeds 253 octets")
	ErrPacketTooLarge = errors.New("radius: encoded packet exceeds 4096 octets")
	ErrUnsignedCode   = errors.New("radius: cannot sign this packet code")
	ErrBadAuth        = errors.New("radius: response authenticator mismatch")
	ErrBadMessageAuth = errors.New("radius: message authenticator mismatch")
)

// Attribute is a single type-length-value triple.
//
// A non-zero Vendor makes it a vendor-specific attribute, carried on the wire
// inside a Vendor-Specific attribute (type 26) as RFC 2865 section 5.26
// describes. Modelling it as a field rather than making callers hand-build the
// enclosing attribute keeps vendor and standard attributes the same shape
// everywhere else.
type Attribute struct {
	Type   AttrType
	Vendor uint32
	Value  []byte
}

// Packet is a decoded RADIUS packet.
type Packet struct {
	Code          Code
	Identifier    uint8
	Authenticator [AuthenticatorLen]byte
	Attributes    []Attribute

	// OmitMessageAuthenticator suppresses the Message-Authenticator that
	// Encode otherwise adds to codes that should carry one. Leave it false
	// unless deliberately testing a peer's handling of its absence.
	OmitMessageAuthenticator bool
}

// New returns a packet of the given code with a freshly generated identifier
// and, for codes that require one, an unpredictable Request Authenticator.
func New(code Code, identifier uint8) (*Packet, error) {
	p := &Packet{Code: code, Identifier: identifier}
	if code.randomAuthenticator() {
		if _, err := rand.Read(p.Authenticator[:]); err != nil {
			return nil, fmt.Errorf("radius: generating authenticator: %w", err)
		}
	}
	return p, nil
}

// Reset prepares p to be built again as a request of the given code and
// identifier, keeping the attribute slice's capacity.
//
// A run sends millions of packets that differ only in their contents, and
// allocating a fresh packet and attribute list for each one costs more than
// signing it. The authenticator is left alone; a code that needs an
// unpredictable one is given it by SetAuthenticator.
func (p *Packet) Reset(code Code, identifier uint8) {
	p.Code = code
	p.Identifier = identifier
	p.Attributes = p.Attributes[:0]
	p.OmitMessageAuthenticator = false
}

// SetAuthenticator draws a fresh Request Authenticator from r.
func (p *Packet) SetAuthenticator(r *Rand) {
	r.Fill(p.Authenticator[:])
}

// Add appends an attribute carrying raw value bytes.
func (p *Packet) Add(t AttrType, v []byte) {
	p.Attributes = append(p.Attributes, Attribute{Type: t, Value: v})
}

// AddVendor appends a vendor-specific attribute.
func (p *Packet) AddVendor(vendor uint32, t AttrType, v []byte) {
	p.Attributes = append(p.Attributes, Attribute{Type: t, Vendor: vendor, Value: v})
}

// AddVendorString appends a vendor-specific text attribute.
func (p *Packet) AddVendorString(vendor uint32, t AttrType, v string) {
	p.AddVendor(vendor, t, []byte(v))
}

// GetVendor returns the value of the first instance of a vendor attribute, or
// nil if absent.
func (p *Packet) GetVendor(vendor uint32, t AttrType) []byte {
	for i := range p.Attributes {
		if p.Attributes[i].Vendor == vendor && p.Attributes[i].Type == t {
			return p.Attributes[i].Value
		}
	}
	return nil
}

// AddString appends a text attribute.
func (p *Packet) AddString(t AttrType, v string) {
	p.Add(t, []byte(v))
}

// AddUint32 appends a 32-bit integer attribute in network byte order.
func (p *Packet) AddUint32(t AttrType, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	p.Add(t, b[:])
}

// Get returns the value of the first instance of t, or nil if absent.
func (p *Packet) Get(t AttrType) []byte {
	for i := range p.Attributes {
		if p.Attributes[i].Vendor == 0 && p.Attributes[i].Type == t {
			return p.Attributes[i].Value
		}
	}
	return nil
}

// GetAll returns the values of every instance of t, in packet order.
func (p *Packet) GetAll(t AttrType) [][]byte {
	var out [][]byte
	for i := range p.Attributes {
		if p.Attributes[i].Vendor == 0 && p.Attributes[i].Type == t {
			out = append(out, p.Attributes[i].Value)
		}
	}
	return out
}

// GetUint32 returns the first instance of t decoded as a 32-bit integer.
func (p *Packet) GetUint32(t AttrType) (uint32, bool) {
	v := p.Get(t)
	if len(v) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(v), true
}

// wantsMessageAuthenticator reports whether Encode should add a
// Message-Authenticator to this packet. Access-Request and Status-Server carry
// one unconditionally so that a peer can reject unauthenticated packets
// outright rather than inferring integrity from the response authenticator.
func (p *Packet) wantsMessageAuthenticator() bool {
	if p.OmitMessageAuthenticator {
		return false
	}
	switch p.Code {
	case CodeAccessRequest, CodeStatusServer:
		return true
	}
	return false
}

// Encode appends the wire form of p to dst and returns the extended slice.
//
// The packet is signed with secret. Codes whose authenticator is derived from
// the packet contents (Accounting-Request, CoA-Request, Disconnect-Request)
// have it computed here; codes carrying an unpredictable Request Authenticator
// must have set it already, which New does.
//
// Encode constructs its hash state per call. Callers sending at rate should
// use an Encoder, which reuses both the hash state and the output buffer.
func (p *Packet) Encode(dst, secret []byte) ([]byte, error) {
	return p.encode(dst, secret, nil, hmac.New(md5.New, secret), md5.New())
}

// EncodeResponse appends the wire form of a reply to dst and returns the
// extended slice.
//
// reqAuthenticator is the authenticator of the request being answered. The
// Response Authenticator is computed over the packet with that value in place,
// per RFC 2865 section 3, which is what lets the peer tie the reply to its
// request and prove we knew the shared secret.
func (p *Packet) EncodeResponse(dst, secret []byte, reqAuthenticator [AuthenticatorLen]byte) ([]byte, error) {
	return p.encode(dst, secret, reqAuthenticator[:], hmac.New(md5.New, secret), md5.New())
}

// encode is the shared body of the encoding entry points. mac must be an
// HMAC-MD5 already keyed with secret; both hashes are reset before use.
//
// respondTo is nil when encoding a request. When encoding a reply it holds the
// authenticator of the request being answered, which both seeds the
// authenticator field and is what the final digest is computed over.
func (p *Packet) encode(dst, secret, respondTo []byte, mac, digest hash.Hash) ([]byte, error) {
	response := respondTo != nil
	if response {
		if len(respondTo) != AuthenticatorLen {
			return nil, ErrUnsignedCode
		}
	} else if !p.Code.isRequest() {
		return nil, ErrUnsignedCode
	}

	start := len(dst)
	dst = append(dst, byte(p.Code), p.Identifier, 0, 0)
	switch {
	case response:
		// The reply carries the request's authenticator while it is signed,
		// and the digest computed over that replaces it below.
		dst = append(dst, respondTo...)
	case p.Code.randomAuthenticator():
		dst = append(dst, p.Authenticator[:]...)
	default:
		// Derived authenticators are computed over a packet whose
		// authenticator field reads as zero, then overwritten below.
		dst = append(dst, zeros[:]...)
	}

	// Offset of the Message-Authenticator value, relative to start, or -1.
	msgAuthOff := -1

	if p.wantsMessageAuthenticator() && !p.hasAttr(AttrMessageAuthenticator) {
		dst = append(dst, byte(AttrMessageAuthenticator), messageAuthenticatorLen)
		msgAuthOff = len(dst) - start
		dst = append(dst, zeros[:]...)
	}

	for _, a := range p.Attributes {
		if len(a.Value) > MaxValueLen {
			return nil, fmt.Errorf("%w: %s is %d octets", ErrValueTooLong, a.Type.name(), len(a.Value))
		}
		if a.Type == AttrMessageAuthenticator {
			// Always transmitted as a zero-filled placeholder; the digest
			// is written once the whole packet is known.
			dst = append(dst, byte(a.Type), messageAuthenticatorLen)
			msgAuthOff = len(dst) - start
			dst = append(dst, zeros[:]...)
			continue
		}
		if a.Vendor != 0 {
			var err error
			if dst, err = appendVSA(dst, a); err != nil {
				return nil, err
			}
			continue
		}
		dst = append(dst, byte(a.Type), byte(len(a.Value)+2))
		dst = append(dst, a.Value...)
	}

	total := len(dst) - start
	if total > MaxPacketLen {
		return nil, fmt.Errorf("%w: %d octets", ErrPacketTooLarge, total)
	}
	binary.BigEndian.PutUint16(dst[start+2:start+4], uint16(total))

	pkt := dst[start:]

	// Order matters. The Message-Authenticator is computed across the packet
	// as it will appear on the wire, which means the authenticator field must
	// already hold its final value: zeros for derived codes, the random value
	// for Access-Request. The derived authenticator is then computed last,
	// over a packet that already carries the completed digest.
	if msgAuthOff >= 0 {
		mac.Reset()
		mac.Write(pkt)
		mac.Sum(pkt[msgAuthOff : msgAuthOff : msgAuthOff+AuthenticatorLen])
	}

	if response || !p.Code.randomAuthenticator() {
		digest.Reset()
		digest.Write(pkt)
		digest.Write(secret)
		digest.Sum(pkt[4 : 4 : 4+AuthenticatorLen])
		copy(p.Authenticator[:], pkt[4:4+AuthenticatorLen])
	}

	return dst, nil
}

// Encoder builds and signs packets for a single shared secret, reusing its
// hash state and output buffer so encoding does not allocate.
//
// An Encoder is not safe for concurrent use. Give each sending worker its own.
type Encoder struct {
	secret []byte
	mac    hash.Hash
	digest hash.Hash
	buf    []byte
}

// NewEncoder returns an Encoder that signs with secret.
func NewEncoder(secret []byte) *Encoder {
	s := append([]byte(nil), secret...)
	return &Encoder{
		secret: s,
		mac:    hmac.New(md5.New, s),
		digest: md5.New(),
		buf:    make([]byte, 0, MaxPacketLen),
	}
}

// Encode returns the wire form of p. The returned slice aliases the Encoder's
// buffer and stays valid only until the next call to Encode.
func (e *Encoder) Encode(p *Packet) ([]byte, error) {
	b, err := p.encode(e.buf[:0], e.secret, nil, e.mac, e.digest)
	if err != nil {
		return nil, err
	}
	e.buf = b
	return b, nil
}

// EncodeResponse returns the wire form of a reply to a request that carried
// reqAuthenticator. The returned slice aliases the Encoder's buffer and stays
// valid only until the next call.
func (e *Encoder) EncodeResponse(p *Packet, reqAuthenticator [AuthenticatorLen]byte) ([]byte, error) {
	b, err := p.encode(e.buf[:0], e.secret, reqAuthenticator[:], e.mac, e.digest)
	if err != nil {
		return nil, err
	}
	e.buf = b
	return b, nil
}

func (p *Packet) hasAttr(t AttrType) bool {
	for i := range p.Attributes {
		if p.Attributes[i].Vendor == 0 && p.Attributes[i].Type == t {
			return true
		}
	}
	return false
}

// Decode parses a RADIUS packet. The returned attribute values alias b, so
// callers that retain the packet beyond the lifetime of the receive buffer
// must copy it first.
func Decode(b []byte) (*Packet, error) {
	p := &Packet{}
	if err := DecodeInto(p, b); err != nil {
		return nil, err
	}
	return p, nil
}

// DecodeInto parses a RADIUS packet into p, reusing its attribute slice.
//
// A receiving loop that decodes into the same packet every time does not
// allocate per reply. As with Decode the attribute values alias b, so p is only
// valid until the receive buffer is overwritten.
func DecodeInto(p *Packet, b []byte) error {
	if len(b) < HeaderLen {
		return ErrShortPacket
	}
	length := int(binary.BigEndian.Uint16(b[2:4]))
	if length < HeaderLen || length > MaxPacketLen {
		return fmt.Errorf("%w: %d", ErrBadLength, length)
	}
	if len(b) < length {
		return ErrTruncated
	}
	b = b[:length]

	p.Code = Code(b[0])
	p.Identifier = b[1]
	p.Attributes = p.Attributes[:0]
	copy(p.Authenticator[:], b[4:HeaderLen])

	for i := HeaderLen; i < length; {
		if i+2 > length {
			return ErrBadAttribute
		}
		alen := int(b[i+1])
		if alen < 2 || i+alen > length {
			return ErrBadAttribute
		}
		if AttrType(b[i]) == AttrVendorSpecific {
			p.Attributes = appendVendorAttributes(p.Attributes, b[i+2:i+alen])
		} else {
			p.Attributes = append(p.Attributes, Attribute{
				Type:  AttrType(b[i]),
				Value: b[i+2 : i+alen],
			})
		}
		i += alen
	}
	return nil
}

// Verifier checks received packets against one shared secret, reusing its hash
// state and scratch buffer so a receive path running at rate does not allocate.
//
// A Verifier is not safe for concurrent use. Give each receiving worker its
// own.
type Verifier struct {
	secret []byte
	mac    hash.Hash
	digest hash.Hash
	buf    []byte
	sum    []byte
}

// NewVerifier returns a Verifier for secret.
func NewVerifier(secret []byte) *Verifier {
	s := append([]byte(nil), secret...)
	return &Verifier{
		secret: s,
		mac:    hmac.New(md5.New, s),
		digest: md5.New(),
		buf:    make([]byte, 0, MaxPacketLen),
		sum:    make([]byte, 0, AuthenticatorLen),
	}
}

// Verify checks a received reply against the request that produced it.
//
// reply is the raw received bytes, reqAuthenticator the authenticator of the
// request, and secret the shared secret. Both the Message-Authenticator (when
// present) and the Response Authenticator are checked. Verification mutates a
// copy, never reply itself.
func Verify(reply []byte, reqAuthenticator [AuthenticatorLen]byte, secret []byte) error {
	return NewVerifier(secret).Verify(reply, reqAuthenticator)
}

// Verify checks a received reply against the request that produced it, as the
// package-level Verify does.
func (v *Verifier) Verify(reply []byte, reqAuthenticator [AuthenticatorLen]byte) error {
	buf, length, err := v.scratch(reply)
	if err != nil {
		return err
	}

	// The digest covers the packet with the request's authenticator in place
	// and the Message-Authenticator value zeroed.
	var received [AuthenticatorLen]byte
	copy(received[:], buf[4:HeaderLen])
	copy(buf[4:HeaderLen], reqAuthenticator[:])

	if err := v.checkMessageAuthenticator(buf, length); err != nil {
		return err
	}

	v.digest.Reset()
	v.digest.Write(buf)
	v.digest.Write(v.secret)
	v.sum = v.digest.Sum(v.sum[:0])
	if !hmac.Equal(received[:], v.sum) {
		return ErrBadAuth
	}
	return nil
}

// scratch copies the packet into the Verifier's buffer, so the checks below can
// zero fields without touching what the caller was given.
func (v *Verifier) scratch(pkt []byte) ([]byte, int, error) {
	if len(pkt) < HeaderLen {
		return nil, 0, ErrShortPacket
	}
	length := int(binary.BigEndian.Uint16(pkt[2:4]))
	if length < HeaderLen || length > MaxPacketLen || len(pkt) < length {
		return nil, 0, ErrBadLength
	}
	v.buf = append(v.buf[:0], pkt[:length]...)
	return v.buf, length, nil
}

// checkMessageAuthenticator verifies the digest in place, if the packet carries
// one, and leaves buf as it found it.
func (v *Verifier) checkMessageAuthenticator(buf []byte, length int) error {
	for i := HeaderLen; i < length; {
		if i+2 > length {
			return ErrBadAttribute
		}
		alen := int(buf[i+1])
		if alen < 2 || i+alen > length {
			return ErrBadAttribute
		}
		if AttrType(buf[i]) == AttrMessageAuthenticator {
			if alen != messageAuthenticatorLen {
				return ErrBadMessageAuth
			}
			var got [AuthenticatorLen]byte
			copy(got[:], buf[i+2:i+alen])
			copy(buf[i+2:i+alen], zeros[:])

			v.mac.Reset()
			v.mac.Write(buf)
			v.sum = v.mac.Sum(v.sum[:0])
			if !hmac.Equal(got[:], v.sum) {
				return ErrBadMessageAuth
			}
			// Restore it; the authenticator digest covers the packet with the
			// completed Message-Authenticator in place.
			copy(buf[i+2:i+alen], got[:])
		}
		i += alen
	}
	return nil
}

// VerifyRequest checks a received request.
//
// For Accounting-Request, CoA-Request and Disconnect-Request the authenticator
// is derived from the packet contents and the shared secret (RFC 2866 section
// 3, RFC 5176 section 3.3), so it proves the sender knew the secret and is
// checked here. Access-Request carries an unpredictable value instead, which
// proves nothing on its own; for those, only a Message-Authenticator can
// establish authenticity, and a caller that requires one should say so by
// checking for its presence.
//
// Verification works on a copy and never mutates req.
func VerifyRequest(req, secret []byte) error {
	return NewVerifier(secret).VerifyRequest(req)
}

// VerifyRequest checks a received request, as the package-level VerifyRequest
// does.
func (v *Verifier) VerifyRequest(req []byte) error {
	buf, length, err := v.scratch(req)
	if err != nil {
		return err
	}

	derived := !Code(buf[0]).randomAuthenticator()

	var received [AuthenticatorLen]byte
	copy(received[:], buf[4:HeaderLen])
	if derived {
		// The sender computed both digests over a packet whose authenticator
		// field read as zero, so reproduce that before checking either.
		copy(buf[4:HeaderLen], zeros[:])
	}

	if err := v.checkMessageAuthenticator(buf, length); err != nil {
		return err
	}

	if !derived {
		return nil
	}

	v.digest.Reset()
	v.digest.Write(buf)
	v.digest.Write(v.secret)
	v.sum = v.digest.Sum(v.sum[:0])
	if !hmac.Equal(received[:], v.sum) {
		return ErrBadAuth
	}
	return nil
}

// name returns the dictionary name of t for error messages, falling back to
// the bare number.
func (t AttrType) name() string {
	if d := stdDict.ByNumber(t); d != nil {
		return d.Name
	}
	return "Attribute(" + itoa(uint32(t)) + ")"
}
