package radius

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"testing"
)

var testSecret = []byte("testing123")

// buildAccessRequest returns an encoded Access-Request with a fixed
// authenticator so tests are deterministic.
func buildAccessRequest(t *testing.T) (*Packet, []byte) {
	t.Helper()
	p := &Packet{Code: CodeAccessRequest, Identifier: 42}
	for i := range p.Authenticator {
		p.Authenticator[i] = byte(i)
	}
	p.AddString(AttrUserName, "subscriber@example.net")
	p.AddUint32(AttrNASPort, 7)

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return p, b
}

func TestEncodeHeader(t *testing.T) {
	p, b := buildAccessRequest(t)

	if got := Code(b[0]); got != CodeAccessRequest {
		t.Errorf("code = %v, want %v", got, CodeAccessRequest)
	}
	if b[1] != 42 {
		t.Errorf("identifier = %d, want 42", b[1])
	}
	if got := int(binary.BigEndian.Uint16(b[2:4])); got != len(b) {
		t.Errorf("length field = %d, want %d", got, len(b))
	}
	if !bytes.Equal(b[4:20], p.Authenticator[:]) {
		t.Error("authenticator field does not match the packet authenticator")
	}
}

// TestMessageAuthenticatorIsAddedFirst pins the placement. A peer that rejects
// unauthenticated requests looks for it as the leading attribute.
func TestMessageAuthenticatorIsAddedFirst(t *testing.T) {
	_, b := buildAccessRequest(t)

	if AttrType(b[HeaderLen]) != AttrMessageAuthenticator {
		t.Fatalf("first attribute = %d, want Message-Authenticator", b[HeaderLen])
	}
	if b[HeaderLen+1] != messageAuthenticatorLen {
		t.Errorf("Message-Authenticator length = %d, want %d", b[HeaderLen+1], messageAuthenticatorLen)
	}
}

// TestMessageAuthenticatorDigest independently restates RFC 3579 section 3.2:
// HMAC-MD5 over the whole packet with the digest field zeroed.
func TestMessageAuthenticatorDigest(t *testing.T) {
	_, b := buildAccessRequest(t)

	off := HeaderLen + 2
	got := append([]byte(nil), b[off:off+AuthenticatorLen]...)

	zeroed := append([]byte(nil), b...)
	copy(zeroed[off:off+AuthenticatorLen], make([]byte, AuthenticatorLen))

	mac := hmac.New(md5.New, testSecret)
	mac.Write(zeroed)
	if want := mac.Sum(nil); !bytes.Equal(got, want) {
		t.Errorf("Message-Authenticator = %x, want %x", got, want)
	}
}

func TestOmitMessageAuthenticator(t *testing.T) {
	p := &Packet{Code: CodeAccessRequest, Identifier: 1, OmitMessageAuthenticator: true}
	p.AddString(AttrUserName, "u")

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if AttrType(b[HeaderLen]) == AttrMessageAuthenticator {
		t.Error("Message-Authenticator present despite being suppressed")
	}
}

// TestAccountingAuthenticator independently restates RFC 2866 section 3: the
// authenticator is MD5 over the packet with a zeroed authenticator field,
// followed by the secret.
func TestAccountingAuthenticator(t *testing.T) {
	p := &Packet{Code: CodeAccountingRequest, Identifier: 9}
	p.AddString(AttrAcctSessionID, "session-1")
	p.AddUint32(AttrAcctStatusType, 1)

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	zeroed := append([]byte(nil), b...)
	copy(zeroed[4:HeaderLen], make([]byte, AuthenticatorLen))

	h := md5.New()
	h.Write(zeroed)
	h.Write(testSecret)
	if want := h.Sum(nil); !bytes.Equal(b[4:HeaderLen], want) {
		t.Errorf("authenticator = %x, want %x", b[4:HeaderLen], want)
	}

	// Encode must also publish the derived value back onto the packet so a
	// caller can verify the eventual response against it.
	if !bytes.Equal(p.Authenticator[:], b[4:HeaderLen]) {
		t.Error("derived authenticator was not stored back on the packet")
	}
}

// TestAccountingCarriesNoMessageAuthenticator guards the default. Adding one
// unprompted to accounting changes what a peer sees on the wire.
func TestAccountingCarriesNoMessageAuthenticator(t *testing.T) {
	p := &Packet{Code: CodeAccountingRequest, Identifier: 1}
	p.AddString(AttrAcctSessionID, "s")

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.Get(AttrMessageAuthenticator) != nil {
		t.Error("accounting request carries an unrequested Message-Authenticator")
	}
}

func TestRoundTrip(t *testing.T) {
	p, b := buildAccessRequest(t)

	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Code != p.Code || got.Identifier != p.Identifier {
		t.Errorf("header = %v/%d, want %v/%d", got.Code, got.Identifier, p.Code, p.Identifier)
	}
	if got.Authenticator != p.Authenticator {
		t.Error("authenticator did not survive the round trip")
	}
	if name := string(got.Get(AttrUserName)); name != "subscriber@example.net" {
		t.Errorf("User-Name = %q, want %q", name, "subscriber@example.net")
	}
	if port, ok := got.GetUint32(AttrNASPort); !ok || port != 7 {
		t.Errorf("NAS-Port = %d/%v, want 7/true", port, ok)
	}
}

func TestEncodeReusesBuffer(t *testing.T) {
	p := &Packet{Code: CodeAccessRequest, Identifier: 1}
	p.AddString(AttrUserName, "u")

	buf := make([]byte, 0, 512)
	for i := 0; i < 3; i++ {
		out, err := p.Encode(buf[:0], testSecret)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if &out[0] != &buf[:1][0] {
			t.Fatal("Encode allocated a new buffer instead of reusing the caller's")
		}
	}
}

// TestEncoderMatchesPacketEncode keeps the reusable fast path and the
// convenience path from drifting apart.
func TestEncoderMatchesPacketEncode(t *testing.T) {
	for _, code := range []Code{CodeAccessRequest, CodeAccountingRequest, CodeCoARequest, CodeDisconnectRequest} {
		t.Run(code.String(), func(t *testing.T) {
			build := func() *Packet {
				p := &Packet{Code: code, Identifier: 3}
				for i := range p.Authenticator {
					p.Authenticator[i] = byte(i)
				}
				p.AddString(AttrUserName, "subscriber@example.net")
				p.AddUint32(AttrNASPort, 12)
				return p
			}

			want, err := build().Encode(nil, testSecret)
			if err != nil {
				t.Fatalf("Packet.Encode: %v", err)
			}
			got, err := NewEncoder(testSecret).Encode(build())
			if err != nil {
				t.Fatalf("Encoder.Encode: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("Encoder produced\n%x\nwant\n%x", got, want)
			}
		})
	}
}

// TestEncoderDoesNotAllocate guards the property the send path depends on.
func TestEncoderDoesNotAllocate(t *testing.T) {
	p := &Packet{Code: CodeAccessRequest, Identifier: 1}
	p.AddString(AttrUserName, "subscriber@example.net")
	p.AddUint32(AttrNASPort, 1)
	enc := NewEncoder(testSecret)

	// Prime the buffer so the first growth is not counted.
	if _, err := enc.Encode(p); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got := testing.AllocsPerRun(100, func() {
		if _, err := enc.Encode(p); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	})
	if got != 0 {
		t.Errorf("allocations per encode = %v, want 0", got)
	}
}

// TestEncoderReusesItsBuffer documents that a returned slice is only valid
// until the next call, which callers must respect when queueing packets.
func TestEncoderReusesItsBuffer(t *testing.T) {
	enc := NewEncoder(testSecret)
	p := &Packet{Code: CodeAccessRequest, Identifier: 1}
	p.AddString(AttrUserName, "u")

	first, err := enc.Encode(p)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	second, err := enc.Encode(p)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if &first[0] != &second[0] {
		t.Error("Encoder did not reuse its buffer")
	}
}

func TestEncodeRejectsOversizeValue(t *testing.T) {
	p := &Packet{Code: CodeAccessRequest, Identifier: 1}
	p.Add(AttrUserName, make([]byte, MaxValueLen+1))

	if _, err := p.Encode(nil, testSecret); !errors.Is(err, ErrValueTooLong) {
		t.Errorf("err = %v, want ErrValueTooLong", err)
	}
}

func TestEncodeRejectsResponseCodes(t *testing.T) {
	p := &Packet{Code: CodeAccessAccept, Identifier: 1}
	if _, err := p.Encode(nil, testSecret); !errors.Is(err, ErrUnsignedCode) {
		t.Errorf("err = %v, want ErrUnsignedCode", err)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	valid := func() []byte {
		_, b := buildAccessRequest(t)
		return b
	}

	tests := []struct {
		name   string
		mangle func([]byte) []byte
		want   error
	}{
		{"short header", func(b []byte) []byte { return b[:HeaderLen-1] }, ErrShortPacket},
		{"length below header", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[2:4], HeaderLen-1)
			return b
		}, ErrBadLength},
		{"length above maximum", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[2:4], MaxPacketLen+1)
			return b
		}, ErrBadLength},
		{"truncated body", func(b []byte) []byte { return b[:len(b)-1] }, ErrTruncated},
		{"attribute length below minimum", func(b []byte) []byte {
			b[HeaderLen+1] = 1
			return b
		}, ErrBadAttribute},
		{"attribute overruns packet", func(b []byte) []byte {
			b[HeaderLen+1] = 255
			return b
		}, ErrBadAttribute},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode(tc.mangle(valid())); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestVerifyRequestAcceptsOurOwnDerivedRequests closes the loop between the
// two directions: anything this package signs as a request must verify as one.
func TestVerifyRequestAcceptsOurOwnDerivedRequests(t *testing.T) {
	for _, code := range []Code{CodeAccountingRequest, CodeCoARequest, CodeDisconnectRequest} {
		t.Run(code.String(), func(t *testing.T) {
			p := &Packet{Code: code, Identifier: 5}
			p.AddString(AttrUserName, "subscriber@example.net")
			p.AddString(AttrAcctSessionID, "ABCD-0001")

			b, err := p.Encode(nil, testSecret)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if err := VerifyRequest(b, testSecret); err != nil {
				t.Errorf("VerifyRequest: %v", err)
			}
		})
	}
}

func TestVerifyRequestRejectsWrongSecret(t *testing.T) {
	p := &Packet{Code: CodeDisconnectRequest, Identifier: 5}
	p.AddString(AttrUserName, "subscriber@example.net")

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := VerifyRequest(b, []byte("wrong")); !errors.Is(err, ErrBadAuth) {
		t.Errorf("err = %v, want ErrBadAuth", err)
	}
}

func TestVerifyRequestRejectsTamperedBody(t *testing.T) {
	p := &Packet{Code: CodeCoARequest, Identifier: 5}
	p.AddString(AttrUserName, "subscriber@example.net")

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b[len(b)-1] ^= 0xff

	if err := VerifyRequest(b, testSecret); !errors.Is(err, ErrBadAuth) {
		t.Errorf("err = %v, want ErrBadAuth", err)
	}
}

// TestVerifyRequestChecksMessageAuthenticator matters for Access-Request,
// where the authenticator is random and proves nothing by itself.
func TestVerifyRequestChecksMessageAuthenticator(t *testing.T) {
	_, b := buildAccessRequest(t)

	if err := VerifyRequest(b, testSecret); err != nil {
		t.Errorf("VerifyRequest on a well-formed Access-Request: %v", err)
	}
	if err := VerifyRequest(b, []byte("wrong")); !errors.Is(err, ErrBadMessageAuth) {
		t.Errorf("err = %v, want ErrBadMessageAuth", err)
	}
}

func TestVerifyRequestLeavesInputUntouched(t *testing.T) {
	p := &Packet{Code: CodeDisconnectRequest, Identifier: 5}
	p.AddString(AttrUserName, "subscriber@example.net")

	b, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	before := append([]byte(nil), b...)

	_ = VerifyRequest(b, testSecret)

	if !bytes.Equal(b, before) {
		t.Error("VerifyRequest mutated its input")
	}
}

// TestEncodeResponseIsVerifiableByThePeer proves a reply we build is one the
// requester will accept, which is what makes acting as a NAS work at all.
func TestEncodeResponseIsVerifiableByThePeer(t *testing.T) {
	req := &Packet{Code: CodeDisconnectRequest, Identifier: 77}
	req.AddString(AttrUserName, "subscriber@example.net")
	if _, err := req.Encode(nil, testSecret); err != nil {
		t.Fatalf("Encode request: %v", err)
	}

	for _, code := range []Code{CodeDisconnectACK, CodeDisconnectNAK, CodeCoAACK, CodeCoANAK} {
		t.Run(code.String(), func(t *testing.T) {
			resp := &Packet{Code: code, Identifier: req.Identifier}
			if code == CodeDisconnectNAK || code == CodeCoANAK {
				resp.AddUint32(AttrErrorCause, 503) // Session-Context-Not-Found
			}

			b, err := resp.EncodeResponse(nil, testSecret, req.Authenticator)
			if err != nil {
				t.Fatalf("EncodeResponse: %v", err)
			}
			// Verify is what the peer would run against the reply.
			if err := Verify(b, req.Authenticator, testSecret); err != nil {
				t.Errorf("peer could not verify our reply: %v", err)
			}
		})
	}
}

func TestEncodeResponseRejectsBadAuthenticatorLength(t *testing.T) {
	p := &Packet{Code: CodeDisconnectACK, Identifier: 1}
	if _, err := p.encode(nil, testSecret, []byte{1, 2, 3}, nil, nil); !errors.Is(err, ErrUnsignedCode) {
		t.Errorf("err = %v, want ErrUnsignedCode", err)
	}
}

// serverReply builds a response the way a compliant server would, so Verify is
// tested against an independently constructed packet rather than our own
// encoder.
func serverReply(t *testing.T, code Code, id uint8, reqAuth [AuthenticatorLen]byte, attrs ...Attribute) []byte {
	t.Helper()

	b := []byte{byte(code), id, 0, 0}
	b = append(b, reqAuth[:]...)
	for _, a := range attrs {
		b = append(b, byte(a.Type), byte(len(a.Value)+2))
		b = append(b, a.Value...)
	}
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))

	h := md5.New()
	h.Write(b)
	h.Write(testSecret)
	copy(b[4:HeaderLen], h.Sum(nil))
	return b
}

func TestVerifyAcceptsValidReply(t *testing.T) {
	p, _ := buildAccessRequest(t)
	reply := serverReply(t, CodeAccessAccept, p.Identifier, p.Authenticator,
		Attribute{Type: AttrFramedIPAddress, Value: []byte{192, 0, 2, 10}})

	if err := Verify(reply, p.Authenticator, testSecret); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	p, _ := buildAccessRequest(t)
	reply := serverReply(t, CodeAccessAccept, p.Identifier, p.Authenticator)

	if err := Verify(reply, p.Authenticator, []byte("wrong")); !errors.Is(err, ErrBadAuth) {
		t.Errorf("err = %v, want ErrBadAuth", err)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	p, _ := buildAccessRequest(t)
	reply := serverReply(t, CodeAccessAccept, p.Identifier, p.Authenticator,
		Attribute{Type: AttrFramedIPAddress, Value: []byte{192, 0, 2, 10}})

	reply[len(reply)-1] ^= 0xff

	if err := Verify(reply, p.Authenticator, testSecret); !errors.Is(err, ErrBadAuth) {
		t.Errorf("err = %v, want ErrBadAuth", err)
	}
}

// TestVerifyLeavesInputUntouched matters because the receive buffer is reused
// and may still be needed for logging after a failed verification.
func TestVerifyLeavesInputUntouched(t *testing.T) {
	p, _ := buildAccessRequest(t)
	reply := serverReply(t, CodeAccessAccept, p.Identifier, p.Authenticator)
	before := append([]byte(nil), reply...)

	_ = Verify(reply, p.Authenticator, testSecret)

	if !bytes.Equal(reply, before) {
		t.Error("Verify mutated its input")
	}
}

func TestVerifierMatchesPackageVerify(t *testing.T) {
	req := benchPacket(CodeAccessRequest)
	if _, err := req.Encode(nil, testSecret); err != nil {
		t.Fatal(err)
	}
	reply := &Packet{Code: CodeAccessAccept, Identifier: req.Identifier}
	wire, err := reply.EncodeResponse(nil, testSecret, req.Authenticator)
	if err != nil {
		t.Fatal(err)
	}

	v := NewVerifier(testSecret)
	// Twice, because a Verifier reuses its hash state and scratch buffer and
	// must give the same answer on every call as the one-shot function does.
	for i := 0; i < 2; i++ {
		if err := v.Verify(wire, req.Authenticator); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := Verify(wire, req.Authenticator, testSecret); err != nil {
		t.Fatal(err)
	}

	wrong := req.Authenticator
	wrong[0]++
	if err := v.Verify(wire, wrong); err == nil {
		t.Error("a reply to a different request verified")
	}
}

func TestDecodeIntoReusesThePacket(t *testing.T) {
	p := benchPacket(CodeAccessRequest)
	wire, err := p.Encode(nil, testSecret)
	if err != nil {
		t.Fatal(err)
	}

	var into Packet
	for i := 0; i < 2; i++ {
		if err := DecodeInto(&into, wire); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if got := len(into.Attributes); got != len(p.Attributes)+1 {
			// The encoded form carries the Message-Authenticator as well.
			t.Fatalf("call %d: got %d attributes, want %d", i, got, len(p.Attributes)+1)
		}
		if string(into.Get(AttrUserName)) != "subscriber@example.net" {
			t.Fatalf("call %d: User-Name did not survive the decode", i)
		}
	}
}

func BenchmarkVerifierAccessAccept(b *testing.B) {
	req := benchPacket(CodeAccessRequest)
	if _, err := req.Encode(nil, testSecret); err != nil {
		b.Fatal(err)
	}
	reply := &Packet{Code: CodeAccessAccept, Identifier: req.Identifier}
	wire, err := reply.EncodeResponse(nil, testSecret, req.Authenticator)
	if err != nil {
		b.Fatal(err)
	}
	v := NewVerifier(testSecret)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.Verify(wire, req.Authenticator); err != nil {
			b.Fatal(err)
		}
	}
}
