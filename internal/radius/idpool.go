package radius

import "math/rand/v2"

// IDSpace is the size of the RADIUS identifier space, which is one octet.
const IDSpace = 256

// IDPool allocates the identifier space of a single socket.
//
// The identifier is only eight bits wide, so one socket can have at most 256
// requests outstanding to one server at a time. That limit is the reason a
// load run spreads itself across many source ports rather than many goroutines
// on one: concurrency comes from sockets, not from workers.
//
// Identifiers are handed out in a shuffled order and returned to the back of
// the queue, so one is reused only after every other free identifier has been.
// That maximal reuse distance is what stops a late reply to a request we have
// already given up on from being matched against a fresh request that happens
// to have inherited its identifier.
//
// An IDPool is not safe for concurrent use. Each socket owns one, driven by
// the single goroutine that owns that socket.
type IDPool struct {
	free  [IDSpace]uint8
	inUse [IDSpace]bool
	head  int
	n     int
}

// NewIDPool returns a pool with every identifier available, in an order
// derived from r. Passing a seeded source makes a run reproducible.
func NewIDPool(r *rand.Rand) *IDPool {
	p := &IDPool{n: IDSpace}
	for i := range p.free {
		p.free[i] = uint8(i)
	}
	if r != nil {
		r.Shuffle(IDSpace, func(i, j int) {
			p.free[i], p.free[j] = p.free[j], p.free[i]
		})
	}
	return p
}

// Available reports how many identifiers are free.
func (p *IDPool) Available() int { return p.n }

// Get takes the next free identifier. It reports false when all 256 are
// outstanding, which is the caller's signal to stop offering load on this
// socket rather than to overwrite a live request.
func (p *IDPool) Get() (uint8, bool) {
	if p.n == 0 {
		return 0, false
	}
	id := p.free[p.head]
	p.head = (p.head + 1) % IDSpace
	p.n--
	p.inUse[id] = true
	return id, true
}

// Put returns id to the pool.
//
// Returning an identifier that is not currently outstanding is ignored rather
// than corrupting the queue, because a duplicate reply arriving after a
// timeout would otherwise free the same identifier twice and let two live
// requests share it.
func (p *IDPool) Put(id uint8) {
	if !p.inUse[id] {
		return
	}
	p.inUse[id] = false
	p.free[(p.head+p.n)%IDSpace] = id
	p.n++
}
