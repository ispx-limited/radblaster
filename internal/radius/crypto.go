package radius

import (
	"crypto/md5"
	"errors"
)

// maxUserPasswordLen is the longest cleartext RFC 2865 section 5.2 admits.
const maxUserPasswordLen = 128

// ErrPasswordTooLong is returned for a cleartext password longer than the
// protocol allows.
var ErrPasswordTooLong = errors.New("radius: password exceeds 128 octets")

// zeros is a shared source of padding, so encoding never allocates just to
// write a run of zero octets.
var zeros [AuthenticatorLen]byte

// EncryptUserPassword returns the obfuscated form of password for a
// User-Password attribute, appended to dst.
//
// The scheme is the MD5 chaining of RFC 2865 section 5.2: the cleartext is
// zero padded to a multiple of 16 octets and each block is XORed with
// MD5(secret + previous ciphertext block), the first block using the request
// authenticator in place of a previous block.
//
// This is obfuscation, not encryption. It protects the password only from
// passive observers who do not know the shared secret.
func EncryptUserPassword(dst, password, secret []byte, reqAuthenticator [AuthenticatorLen]byte) ([]byte, error) {
	if len(password) > maxUserPasswordLen {
		return nil, ErrPasswordTooLong
	}

	// An empty password still occupies one block.
	blocks := (len(password) + AuthenticatorLen - 1) / AuthenticatorLen
	if blocks == 0 {
		blocks = 1
	}
	padded := blocks * AuthenticatorLen

	start := len(dst)
	dst = append(dst, password...)
	dst = append(dst, zeros[:padded-len(password)]...)
	out := dst[start : start+padded]

	// Every block digests the secret followed by the previous block, which
	// differs only in its last sixteen octets. Assembling that input once and
	// rewriting the tail lets each block use md5.Sum, whose state lives on the
	// stack, where a fresh hash per block would allocate on a path that runs
	// for every Access-Request.
	var arr [scratchLen]byte
	scratch := scratchFor(arr[:], secret)
	tail := scratch[len(secret):]

	prev := reqAuthenticator[:]
	for off := 0; off < padded; off += AuthenticatorLen {
		copy(tail, prev)
		digest := md5.Sum(scratch)
		for i := 0; i < AuthenticatorLen; i++ {
			out[off+i] ^= digest[i]
		}
		prev = out[off : off+AuthenticatorLen]
	}
	return dst, nil
}

// scratchLen sizes the stack buffer the password digests are assembled in. It
// covers a secret of up to 128 octets plus the sixteen octet block; a longer
// secret falls back to the heap, which no sensible deployment reaches.
const scratchLen = 128 + AuthenticatorLen

// scratchFor returns a buffer holding secret followed by room for one block,
// using arr when the secret fits.
func scratchFor(arr, secret []byte) []byte {
	n := len(secret) + AuthenticatorLen
	if n > len(arr) {
		return append(make([]byte, 0, n), secret...)[:n]
	}
	copy(arr, secret)
	return arr[:n]
}

// DecryptUserPassword reverses EncryptUserPassword, returning the cleartext
// appended to dst with padding removed at the first zero octet.
func DecryptUserPassword(dst, encrypted, secret []byte, reqAuthenticator [AuthenticatorLen]byte) ([]byte, error) {
	if len(encrypted) == 0 || len(encrypted)%AuthenticatorLen != 0 {
		return nil, errors.New("radius: encrypted password is not a whole number of blocks")
	}

	start := len(dst)
	var arr [scratchLen]byte
	scratch := scratchFor(arr[:], secret)
	tail := scratch[len(secret):]

	prev := reqAuthenticator[:]
	for off := 0; off < len(encrypted); off += AuthenticatorLen {
		copy(tail, prev)
		digest := md5.Sum(scratch)
		for i := 0; i < AuthenticatorLen; i++ {
			dst = append(dst, encrypted[off+i]^digest[i])
		}
		prev = encrypted[off : off+AuthenticatorLen]
	}

	// Trailing padding is zero octets; a password cannot contain one.
	out := dst[start:]
	for i, b := range out {
		if b == 0 {
			return dst[:start+i], nil
		}
	}
	return dst, nil
}

// CHAPPassword returns the 17 octet CHAP-Password value for the given
// identifier, cleartext password and challenge, as defined by RFC 1994 and
// carried by RFC 2865 section 5.3.
//
// Per RFC 2865, when no CHAP-Challenge attribute is sent the request
// authenticator serves as the challenge.
func CHAPPassword(ident byte, password, challenge []byte) [17]byte {
	var out [17]byte
	out[0] = ident
	h := md5.New()
	h.Write([]byte{ident})
	h.Write(password)
	h.Write(challenge)
	h.Sum(out[1:1:17])
	return out
}
