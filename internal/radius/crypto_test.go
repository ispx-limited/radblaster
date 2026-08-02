package radius

import (
	"bytes"
	"crypto/md5"
	"errors"
	"strings"
	"testing"
)

func testAuthenticator() [AuthenticatorLen]byte {
	var a [AuthenticatorLen]byte
	for i := range a {
		a[i] = byte(0xa0 + i)
	}
	return a
}

func TestUserPasswordRoundTrip(t *testing.T) {
	auth := testAuthenticator()

	for _, pw := range []string{
		"",
		"x",
		"DingDong",
		"123456789012345",  // one octet short of a block
		"1234567890123456", // exactly one block
		"12345678901234567",
		strings.Repeat("p", maxUserPasswordLen),
	} {
		t.Run(itoa(uint32(len(pw))), func(t *testing.T) {
			enc, err := EncryptUserPassword(nil, []byte(pw), testSecret, auth)
			if err != nil {
				t.Fatalf("EncryptUserPassword: %v", err)
			}
			if len(enc)%AuthenticatorLen != 0 || len(enc) == 0 {
				t.Fatalf("ciphertext length %d is not a positive multiple of %d", len(enc), AuthenticatorLen)
			}

			got, err := DecryptUserPassword(nil, enc, testSecret, auth)
			if err != nil {
				t.Fatalf("DecryptUserPassword: %v", err)
			}
			if string(got) != pw {
				t.Errorf("round trip = %q, want %q", got, pw)
			}
		})
	}
}

// TestUserPasswordChaining independently restates RFC 2865 section 5.2:
// b1 = MD5(secret + request authenticator), c1 = p1 XOR b1, and each later
// block keys off the preceding ciphertext block.
func TestUserPasswordChaining(t *testing.T) {
	auth := testAuthenticator()
	pw := []byte("a password longer than one block")

	enc, err := EncryptUserPassword(nil, pw, testSecret, auth)
	if err != nil {
		t.Fatalf("EncryptUserPassword: %v", err)
	}
	if len(enc) != 32 {
		t.Fatalf("ciphertext length = %d, want 32", len(enc))
	}

	padded := make([]byte, 32)
	copy(padded, pw)

	h := md5.New()
	h.Write(testSecret)
	h.Write(auth[:])
	b1 := h.Sum(nil)
	for i := 0; i < AuthenticatorLen; i++ {
		if want := padded[i] ^ b1[i]; enc[i] != want {
			t.Fatalf("block 1 octet %d = %#x, want %#x", i, enc[i], want)
		}
	}

	h.Reset()
	h.Write(testSecret)
	h.Write(enc[:AuthenticatorLen])
	b2 := h.Sum(nil)
	for i := 0; i < AuthenticatorLen; i++ {
		if want := padded[AuthenticatorLen+i] ^ b2[i]; enc[AuthenticatorLen+i] != want {
			t.Fatalf("block 2 octet %d = %#x, want %#x", i, enc[AuthenticatorLen+i], want)
		}
	}
}

// TestUserPasswordVariesWithAuthenticator is the property that makes the
// scheme worth anything: the same password must not produce the same
// ciphertext across sessions.
func TestUserPasswordVariesWithAuthenticator(t *testing.T) {
	a := testAuthenticator()
	b := testAuthenticator()
	b[0] ^= 0xff

	first, err := EncryptUserPassword(nil, []byte("same"), testSecret, a)
	if err != nil {
		t.Fatalf("EncryptUserPassword: %v", err)
	}
	second, err := EncryptUserPassword(nil, []byte("same"), testSecret, b)
	if err != nil {
		t.Fatalf("EncryptUserPassword: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Error("ciphertext did not change with the request authenticator")
	}
}

func TestUserPasswordRejectsOverlongInput(t *testing.T) {
	auth := testAuthenticator()
	pw := make([]byte, maxUserPasswordLen+1)

	if _, err := EncryptUserPassword(nil, pw, testSecret, auth); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("err = %v, want ErrPasswordTooLong", err)
	}
}

func TestDecryptUserPasswordRejectsPartialBlock(t *testing.T) {
	auth := testAuthenticator()
	if _, err := DecryptUserPassword(nil, make([]byte, 17), testSecret, auth); err == nil {
		t.Error("expected an error for a ciphertext that is not a whole number of blocks")
	}
	if _, err := DecryptUserPassword(nil, nil, testSecret, auth); err == nil {
		t.Error("expected an error for an empty ciphertext")
	}
}

// TestCHAPPassword independently restates RFC 1994: the response is the
// identifier followed by MD5 over identifier, password and challenge.
func TestCHAPPassword(t *testing.T) {
	const ident = 0x7f
	pw := []byte("DingDong")
	challenge := []byte("0123456789abcdef")

	got := CHAPPassword(ident, pw, challenge)

	if got[0] != ident {
		t.Errorf("identifier octet = %#x, want %#x", got[0], ident)
	}

	h := md5.New()
	h.Write([]byte{ident})
	h.Write(pw)
	h.Write(challenge)
	if want := h.Sum(nil); !bytes.Equal(got[1:], want) {
		t.Errorf("digest = %x, want %x", got[1:], want)
	}
}

func TestCHAPPasswordVariesWithIdentifier(t *testing.T) {
	pw := []byte("DingDong")
	challenge := []byte("0123456789abcdef")

	first := CHAPPassword(1, pw, challenge)
	second := CHAPPassword(2, pw, challenge)

	if bytes.Equal(first[1:], second[1:]) {
		t.Error("digest did not change with the identifier")
	}
}

func benchPacket(code Code) *Packet {
	p := &Packet{Code: code, Identifier: 1}
	p.AddString(AttrUserName, "subscriber@example.net")
	p.AddString(AttrCallingStationID, "00:1a:2b:3c:4d:5e")
	p.AddUint32(AttrNASPort, 1)
	return p
}

func BenchmarkEncoderAccessRequest(b *testing.B) {
	p := benchPacket(CodeAccessRequest)
	enc := NewEncoder(testSecret)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncoderAccountingRequest(b *testing.B) {
	p := benchPacket(CodeAccountingRequest)
	enc := NewEncoder(testSecret)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncryptUserPassword(b *testing.B) {
	var auth [AuthenticatorLen]byte
	dst := make([]byte, 0, MaxValueLen)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		if dst, err = EncryptUserPassword(dst[:0], []byte("subscriber-secret"), testSecret, auth); err != nil {
			b.Fatal(err)
		}
	}
}
