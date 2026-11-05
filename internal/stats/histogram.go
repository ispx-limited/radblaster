// Package stats records load-test measurements.
package stats

import "math/bits"

// Bucketing parameters. Each power of two is divided into subCount buckets,
// which bounds the relative error of any recorded value at roughly
// 1/subCount, or about 1.6 percent, at every magnitude.
//
// This matters more than it might appear. A scheme with fixed absolute
// precision, or one that keeps a fixed number of significant digits per
// decade, loses resolution exactly where a RADIUS latency distribution is
// interesting: a healthy authentication is a few milliseconds, so the
// difference between a p99 of 8ms and one of 12ms is the whole finding.
const (
	subBits  = 6
	subCount = 1 << subBits

	// maxExponent bounds the recordable range at 2^48 nanoseconds, a little
	// over three days. Anything beyond is clamped rather than dropped.
	maxExponent = 48

	bucketCount = subCount + (maxExponent-subBits+1)*subCount
)

// Histogram records a distribution of durations in nanoseconds.
//
// Values are quantised once, on the way in. Every operation afterwards is
// exact addition, so merging histograms from many workers gives precisely the
// same result as if one worker had recorded everything. There is no second
// approximation introduced by combining them.
//
// A Histogram is not safe for concurrent use. Give each worker its own and
// merge them when reporting.
type Histogram struct {
	counts [bucketCount]uint64
	count  uint64
	sum    uint64
	min    uint64
	max    uint64
}

// NewHistogram returns an empty histogram.
func NewHistogram() *Histogram {
	return &Histogram{min: ^uint64(0)}
}

// bucketOf returns the bucket index for v nanoseconds.
func bucketOf(v uint64) int {
	if v < subCount {
		return int(v)
	}
	e := bits.Len64(v) - 1
	if e > maxExponent {
		return bucketCount - 1
	}
	shift := uint(e - subBits)
	sub := (v >> shift) - subCount
	return subCount + (e-subBits)*subCount + int(sub)
}

// valueOf returns the representative value for a bucket, being the midpoint of
// the range it covers. Using the midpoint rather than either edge keeps the
// reported percentile unbiased in both directions.
func valueOf(i int) uint64 {
	if i < subCount {
		return uint64(i)
	}
	octave := (i - subCount) / subCount
	sub := uint64((i - subCount) % subCount)
	shift := uint(octave)
	lo := (subCount + sub) << shift
	return lo + (uint64(1)<<shift)/2
}

// Record adds one observation of v nanoseconds.
func (h *Histogram) Record(v uint64) {
	h.counts[bucketOf(v)]++
	h.count++
	h.sum += v
	if v < h.min {
		h.min = v
	}
	if v > h.max {
		h.max = v
	}
}

// Merge folds other into h.
func (h *Histogram) Merge(other *Histogram) {
	for i := range other.counts {
		h.counts[i] += other.counts[i]
	}
	h.count += other.count
	h.sum += other.sum
	if other.count > 0 {
		if other.min < h.min {
			h.min = other.min
		}
		if other.max > h.max {
			h.max = other.max
		}
	}
}

// Reset returns the histogram to its empty state, retaining its storage.
func (h *Histogram) Reset() {
	h.counts = [bucketCount]uint64{}
	h.count = 0
	h.sum = 0
	h.min = ^uint64(0)
	h.max = 0
}

// Count returns the number of recorded observations.
func (h *Histogram) Count() uint64 { return h.count }

// Min returns the smallest recorded value exactly, or zero if empty.
func (h *Histogram) Min() uint64 {
	if h.count == 0 {
		return 0
	}
	return h.min
}

// Max returns the largest recorded value exactly, or zero if empty.
func (h *Histogram) Max() uint64 { return h.max }

// Mean returns the arithmetic mean, computed from the exact sum rather than
// from the buckets, or zero if empty.
func (h *Histogram) Mean() float64 {
	if h.count == 0 {
		return 0
	}
	return float64(h.sum) / float64(h.count)
}

// Quantile returns the value at q, where q is in the range 0 to 1.
//
// The result is quantised, so it carries the histogram's relative error. Min
// and Max are tracked exactly and are returned for q of 0 and 1 respectively,
// which also keeps a reported percentile from ever exceeding the observed
// maximum.
func (h *Histogram) Quantile(q float64) uint64 {
	if h.count == 0 {
		return 0
	}
	switch {
	case q <= 0:
		return h.min
	case q >= 1:
		return h.max
	}

	// Nearest-rank: the smallest value whose cumulative count reaches the
	// target rank.
	rank := uint64(q*float64(h.count) + 0.5)
	if rank == 0 {
		rank = 1
	}

	var seen uint64
	for i := range h.counts {
		seen += h.counts[i]
		if seen >= rank {
			v := valueOf(i)
			if v < h.min {
				return h.min
			}
			if v > h.max {
				return h.max
			}
			return v
		}
	}
	return h.max
}
