package radius

import "crypto/rand"

// randBlock is how much unpredictable material a Rand fetches at a time. Large
// enough that the cost of asking the operating system disappears against the
// packets it fills, small enough to stay in cache.
const randBlock = 4096

// Rand supplies the unpredictable values RADIUS requires, drawn from the
// operating system in blocks rather than one value at a time.
//
// Every Access-Request needs a fresh sixteen octet Request Authenticator, and
// at rate the call that fetches those sixteen octets costs more than encoding
// the packet they go into. Buffering does not weaken them: they are the same
// bytes the operating system would have returned one at a time, fetched in
// fewer calls and never handed out twice. It does mean a block of unused
// material sits in this process's memory, which is a trade a load generator can
// make and a server holding real credentials could not.
//
// A Rand is not safe for concurrent use. Give each sending worker its own.
type Rand struct {
	buf [randBlock]byte
	off int
}

// NewRand returns a Rand with its first block already drawn.
func NewRand() *Rand {
	r := &Rand{}
	r.refill()
	return r
}

func (r *Rand) refill() {
	// crypto/rand.Read does not return an error. It terminates the process if
	// the operating system cannot supply randomness, on the grounds that
	// continuing without it is worse, so there is nothing to handle here.
	_, _ = rand.Read(r.buf[:])
	r.off = 0
}

// Fill overwrites b with unpredictable bytes.
func (r *Rand) Fill(b []byte) {
	for len(b) > 0 {
		if r.off == len(r.buf) {
			r.refill()
		}
		n := copy(b, r.buf[r.off:])
		// Wipe what was handed out, so the block only ever holds material that
		// has not been used yet.
		clear(r.buf[r.off : r.off+n])
		r.off += n
		b = b[n:]
	}
}

// Uint8 returns one unpredictable octet.
func (r *Rand) Uint8() uint8 {
	var b [1]byte
	r.Fill(b[:])
	return b[0]
}
