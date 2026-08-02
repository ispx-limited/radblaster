package radius

import (
	"bytes"
	"testing"
)

func TestRandDoesNotRepeat(t *testing.T) {
	r := NewRand()

	// Draw more than one block's worth, so the refill boundary is crossed
	// several times, and check nothing came back twice.
	const draws = randBlock/AuthenticatorLen*3 + 7
	seen := make(map[[AuthenticatorLen]byte]int, draws)
	for i := 0; i < draws; i++ {
		var a [AuthenticatorLen]byte
		r.Fill(a[:])
		if prev, ok := seen[a]; ok {
			t.Fatalf("draw %d repeated draw %d", i, prev)
		}
		seen[a] = i
	}
}

func TestRandFillsValuesLongerThanABlock(t *testing.T) {
	r := NewRand()
	b := make([]byte, randBlock+randBlock/2)
	r.Fill(b)

	// A fill that spans a refill must leave no part of the buffer untouched.
	// Zeros are legitimate output, so look for a run long enough that random
	// bytes would not produce it.
	if bytes.Contains(b, make([]byte, 64)) {
		t.Error("fill left a long run of zeros, so part of it was not written")
	}
}

func TestRandWipesWhatItHandsOut(t *testing.T) {
	r := NewRand()
	var a [AuthenticatorLen]byte
	r.Fill(a[:])

	// What was handed out must no longer be sitting in the block behind it.
	if bytes.Contains(r.buf[:], a[:]) {
		t.Error("material handed out is still in the buffer")
	}
}

func BenchmarkRandAuthenticator(b *testing.B) {
	r := NewRand()
	var a [AuthenticatorLen]byte

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Fill(a[:])
	}
}
