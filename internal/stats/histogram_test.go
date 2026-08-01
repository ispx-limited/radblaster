package stats

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

// relativeError is the accuracy the bucketing promises.
const relativeError = 1.0 / subCount

func TestEmptyHistogram(t *testing.T) {
	h := NewHistogram()

	if h.Count() != 0 || h.Min() != 0 || h.Max() != 0 || h.Mean() != 0 {
		t.Errorf("empty histogram = count %d, min %d, max %d, mean %v; want all zero",
			h.Count(), h.Min(), h.Max(), h.Mean())
	}
	for _, q := range []float64{0, 0.5, 0.99, 1} {
		if got := h.Quantile(q); got != 0 {
			t.Errorf("Quantile(%v) on empty = %d, want 0", q, got)
		}
	}
}

// TestSmallValuesAreExact matters because sub-microsecond resolution is the
// point of the linear region: those values must not be quantised at all.
func TestSmallValuesAreExact(t *testing.T) {
	for v := uint64(0); v < subCount; v++ {
		h := NewHistogram()
		h.Record(v)
		if got := h.Quantile(0.5); got != v {
			t.Errorf("Quantile(0.5) after recording %d = %d, want exact", v, got)
		}
	}
}

func TestBucketRoundTripWithinErrorBound(t *testing.T) {
	values := []uint64{
		0, 1, 63, 64, 65, 127, 128, 129, 255, 1000, 9999,
		1_000_000,      // 1ms
		2_500_000,      // 2.5ms
		8_000_000,      // 8ms
		12_000_000,     // 12ms
		1_000_000_000,  // 1s
		30_000_000_000, // 30s
	}

	for _, v := range values {
		got := valueOf(bucketOf(v))
		if v < subCount {
			if got != v {
				t.Errorf("valueOf(bucketOf(%d)) = %d, want exact", v, got)
			}
			continue
		}
		diff := math.Abs(float64(got)-float64(v)) / float64(v)
		if diff > relativeError {
			t.Errorf("valueOf(bucketOf(%d)) = %d, relative error %.4f exceeds %.4f",
				v, got, diff, relativeError)
		}
	}
}

// TestDistinguishesNearbyMillisecondLatencies is the property that justifies
// the bucketing scheme: 8ms and 12ms must not collapse together.
func TestDistinguishesNearbyMillisecondLatencies(t *testing.T) {
	a := valueOf(bucketOf(8_000_000))
	b := valueOf(bucketOf(12_000_000))
	if a == b {
		t.Fatal("8ms and 12ms fell into the same bucket")
	}
	if a >= b {
		t.Errorf("bucket ordering broken: 8ms -> %d, 12ms -> %d", a, b)
	}
}

func TestQuantileAgainstExactSort(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	// A plausible latency shape: a tight body with a long tail.
	raw := make([]uint64, 0, 100_000)
	h := NewHistogram()
	for i := 0; i < 100_000; i++ {
		var v uint64
		switch {
		case i%1000 == 0:
			v = uint64(50_000_000 + rng.Intn(50_000_000)) // 50 to 100ms tail
		case i%10 == 0:
			v = uint64(5_000_000 + rng.Intn(10_000_000)) // 5 to 15ms
		default:
			v = uint64(500_000 + rng.Intn(2_000_000)) // 0.5 to 2.5ms
		}
		raw = append(raw, v)
		h.Record(v)
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i] < raw[j] })

	for _, q := range []float64{0.5, 0.9, 0.95, 0.99, 0.999} {
		rank := int(q*float64(len(raw)) + 0.5)
		if rank == 0 {
			rank = 1
		}
		want := raw[rank-1]
		got := h.Quantile(q)

		diff := math.Abs(float64(got)-float64(want)) / float64(want)
		if diff > relativeError {
			t.Errorf("Quantile(%v) = %d, exact = %d, relative error %.4f exceeds %.4f",
				q, got, want, diff, relativeError)
		}
	}
}

func TestMinMaxAndMeanAreExact(t *testing.T) {
	h := NewHistogram()
	values := []uint64{7_123_456, 1_000_001, 99_999_999, 42}

	var sum uint64
	for _, v := range values {
		h.Record(v)
		sum += v
	}

	if got := h.Min(); got != 42 {
		t.Errorf("Min = %d, want 42", got)
	}
	if got := h.Max(); got != 99_999_999 {
		t.Errorf("Max = %d, want 99999999", got)
	}
	if want := float64(sum) / float64(len(values)); h.Mean() != want {
		t.Errorf("Mean = %v, want %v", h.Mean(), want)
	}
	if got := h.Quantile(0); got != 42 {
		t.Errorf("Quantile(0) = %d, want the exact minimum 42", got)
	}
	if got := h.Quantile(1); got != 99_999_999 {
		t.Errorf("Quantile(1) = %d, want the exact maximum", got)
	}
}

// TestQuantileNeverExceedsObservedRange guards against the classic bucketing
// bug where a quantised percentile is reported above the real maximum.
func TestQuantileNeverExceedsObservedRange(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	h := NewHistogram()
	var lo, hi uint64 = ^uint64(0), 0

	for i := 0; i < 10_000; i++ {
		v := uint64(rng.Intn(50_000_000))
		h.Record(v)
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}

	for q := 0.0; q <= 1.0; q += 0.01 {
		got := h.Quantile(q)
		if got < lo || got > hi {
			t.Fatalf("Quantile(%.2f) = %d, outside observed range [%d, %d]", q, got, lo, hi)
		}
	}
}

// TestMergeIsExact is the property that lets workers keep independent
// histograms: merging must equal recording everything in one place.
func TestMergeIsExact(t *testing.T) {
	rng := rand.New(rand.NewSource(3))

	combined := NewHistogram()
	shards := []*Histogram{NewHistogram(), NewHistogram(), NewHistogram()}

	for i := 0; i < 30_000; i++ {
		v := uint64(rng.Intn(20_000_000))
		combined.Record(v)
		shards[i%len(shards)].Record(v)
	}

	merged := NewHistogram()
	for _, s := range shards {
		merged.Merge(s)
	}

	if merged.Count() != combined.Count() {
		t.Fatalf("merged count = %d, want %d", merged.Count(), combined.Count())
	}
	if merged.Min() != combined.Min() || merged.Max() != combined.Max() {
		t.Errorf("merged range = [%d, %d], want [%d, %d]",
			merged.Min(), merged.Max(), combined.Min(), combined.Max())
	}
	if merged.Mean() != combined.Mean() {
		t.Errorf("merged mean = %v, want %v", merged.Mean(), combined.Mean())
	}
	for _, q := range []float64{0, 0.5, 0.9, 0.99, 0.999, 1} {
		if got, want := merged.Quantile(q), combined.Quantile(q); got != want {
			t.Errorf("merged Quantile(%v) = %d, want %d", q, got, want)
		}
	}
}

func TestMergeWithEmptyLeavesRangeAlone(t *testing.T) {
	h := NewHistogram()
	h.Record(5_000_000)
	h.Merge(NewHistogram())

	if h.Min() != 5_000_000 || h.Max() != 5_000_000 {
		t.Errorf("range = [%d, %d] after merging an empty histogram, want [5000000, 5000000]",
			h.Min(), h.Max())
	}
}

func TestReset(t *testing.T) {
	h := NewHistogram()
	for i := 0; i < 100; i++ {
		h.Record(uint64(i) * 1_000_000)
	}
	h.Reset()

	if h.Count() != 0 || h.Max() != 0 || h.Quantile(0.99) != 0 {
		t.Error("histogram was not fully cleared by Reset")
	}

	h.Record(3_000_000)
	if h.Count() != 1 || h.Min() != 3_000_000 {
		t.Error("histogram did not record correctly after Reset")
	}
}

func TestClampsBeyondRange(t *testing.T) {
	h := NewHistogram()
	huge := uint64(1) << 60

	h.Record(huge)
	if h.Count() != 1 {
		t.Fatalf("count = %d, want 1", h.Count())
	}
	if h.Max() != huge {
		t.Errorf("Max = %d, want the exact value %d", h.Max(), huge)
	}
}

func BenchmarkRecord(b *testing.B) {
	h := NewHistogram()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Record(uint64(i%50_000_000) + 1)
	}
}
