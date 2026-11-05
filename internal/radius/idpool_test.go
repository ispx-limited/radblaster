package radius

import (
	"math/rand/v2"
	"testing"
)

func testRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

func TestIDPoolExhaustsExactlyOnce(t *testing.T) {
	p := NewIDPool(testRand())

	seen := make(map[uint8]bool, IDSpace)
	for i := 0; i < IDSpace; i++ {
		id, ok := p.Get()
		if !ok {
			t.Fatalf("Get failed after %d identifiers, want %d available", i, IDSpace)
		}
		if seen[id] {
			t.Fatalf("identifier %d handed out twice", id)
		}
		seen[id] = true
	}

	if _, ok := p.Get(); ok {
		t.Error("Get succeeded with the space exhausted")
	}
	if p.Available() != 0 {
		t.Errorf("Available = %d, want 0", p.Available())
	}
	if len(seen) != IDSpace {
		t.Errorf("handed out %d distinct identifiers, want %d", len(seen), IDSpace)
	}
}

// TestIDPoolReuseDistanceIsMaximal is the property that protects against
// matching a late reply to the wrong request: a returned identifier must go to
// the back of the queue, not the front.
func TestIDPoolReuseDistanceIsMaximal(t *testing.T) {
	p := NewIDPool(testRand())

	first, _ := p.Get()
	p.Put(first)

	// With one identifier returned and 255 still free, the returned one must
	// be the last of the 256 to come back out.
	for i := 0; i < IDSpace-1; i++ {
		id, ok := p.Get()
		if !ok {
			t.Fatalf("Get failed at %d", i)
		}
		if id == first {
			t.Fatalf("identifier %d was reused after only %d allocations, want %d", first, i+1, IDSpace-1)
		}
	}

	last, ok := p.Get()
	if !ok {
		t.Fatal("Get failed for the final identifier")
	}
	if last != first {
		t.Errorf("final identifier = %d, want the returned %d", last, first)
	}
}

func TestIDPoolPutRestoresAvailability(t *testing.T) {
	p := NewIDPool(testRand())

	ids := make([]uint8, 0, 10)
	for i := 0; i < 10; i++ {
		id, _ := p.Get()
		ids = append(ids, id)
	}
	if got := p.Available(); got != IDSpace-10 {
		t.Fatalf("Available = %d, want %d", got, IDSpace-10)
	}

	for _, id := range ids {
		p.Put(id)
	}
	if got := p.Available(); got != IDSpace {
		t.Errorf("Available = %d after returning everything, want %d", got, IDSpace)
	}
}

// TestIDPoolIgnoresDoubleFree matters because a duplicate reply arriving after
// a timeout would otherwise free the same identifier twice, letting two live
// requests share one.
func TestIDPoolIgnoresDoubleFree(t *testing.T) {
	p := NewIDPool(testRand())

	id, _ := p.Get()
	p.Put(id)
	p.Put(id)
	p.Put(id)

	if got := p.Available(); got != IDSpace {
		t.Fatalf("Available = %d after a double free, want %d", got, IDSpace)
	}

	seen := make(map[uint8]bool, IDSpace)
	for i := 0; i < IDSpace; i++ {
		got, ok := p.Get()
		if !ok {
			t.Fatalf("Get failed at %d, the queue was corrupted by the double free", i)
		}
		if seen[got] {
			t.Fatalf("identifier %d handed out twice after a double free", got)
		}
		seen[got] = true
	}
}

func TestIDPoolIgnoresPutOfNeverAllocated(t *testing.T) {
	p := NewIDPool(testRand())
	p.Put(99)

	if got := p.Available(); got != IDSpace {
		t.Errorf("Available = %d after returning an unallocated identifier, want %d", got, IDSpace)
	}
}

// TestIDPoolShuffleIsSeeded keeps runs reproducible, which regression tests
// depend on.
func TestIDPoolShuffleIsSeeded(t *testing.T) {
	first := NewIDPool(rand.New(rand.NewPCG(7, 7)))
	second := NewIDPool(rand.New(rand.NewPCG(7, 7)))

	for i := 0; i < IDSpace; i++ {
		a, _ := first.Get()
		b, _ := second.Get()
		if a != b {
			t.Fatalf("identifier %d differs between equally seeded pools: %d and %d", i, a, b)
		}
	}
}

func TestIDPoolShuffleDiffersBySeed(t *testing.T) {
	first := NewIDPool(rand.New(rand.NewPCG(1, 1)))
	second := NewIDPool(rand.New(rand.NewPCG(2, 2)))

	same := 0
	for i := 0; i < IDSpace; i++ {
		a, _ := first.Get()
		b, _ := second.Get()
		if a == b {
			same++
		}
	}
	if same == IDSpace {
		t.Error("differently seeded pools produced an identical order")
	}
}

func BenchmarkIDPoolGetPut(b *testing.B) {
	p := NewIDPool(testRand())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, ok := p.Get()
		if !ok {
			b.Fatal("pool exhausted")
		}
		p.Put(id)
	}
}
