package sync

import (
	"sync"

	"github.com/celestiaorg/go-header"
)

// ranges keeps non-overlapping and non-adjacent header ranges which are used to cache headers (in
// ascending order). This prevents unnecessary / duplicate network requests for additional headers
// during sync.
type ranges[H header.Header] struct {
	lk     sync.RWMutex
	ranges []*headerRange[H]
}

// Head returns the highest Header in all ranges if any.
func (rs *ranges[H]) Head() H {
	rs.lk.RLock()
	defer rs.lk.RUnlock()

	ln := len(rs.ranges)
	if ln == 0 {
		var zero H
		return zero
	}

	head := rs.ranges[ln-1]
	return head.Head()
}

// Add appends the new Header to existing range or starts a new one.
// It starts a new one if the new Header is not adjacent to any of existing ranges.
func (rs *ranges[H]) Add(h H) {
	head := rs.Head()

	// short-circuit if header is from the past
	if !head.IsZero() && head.Height() >= h.Height() {
		// TODO(@Wondertan): Technically, we can still apply the header:
		//  * Headers here are verified, so we can trust them
		//  * PubSub does not guarantee the ordering of msgs
		//    * So there might be a case where ordering is broken
		//    * Even considering the delay(block time) with which new headers are generated
		//    * But rarely
		//  Would be still nice to implement
		log.Warnf("rcvd headers in wrong order")
		return
	}

	rs.lk.Lock()
	defer rs.lk.Unlock()

	// if the new header is adjacent to head
	if !head.IsZero() && h.Height() == head.Height()+1 {
		// append it to the last known range
		rs.ranges[len(rs.ranges)-1].Append(h)
	} else {
		// otherwise, start a new range
		rs.ranges = append(rs.ranges, newRange[H](h))

		// it is possible to miss a header or few from PubSub, due to quick disconnects or sleep
		// once we start rcving them again we save those in new range
		// so 'Syncer.findHeaders' can fetch what was missed
	}
}

// FirstRangeWithin checks if the first range is within a given height span [start:end]
// and returns it.
func (rs *ranges[H]) FirstRangeWithin(start, end uint64) (*headerRange[H], bool) {
	r, ok := rs.First()
	if !ok {
		return nil, false
	}

	if r.start >= start && r.start <= end {
		return r, true
	}

	return nil, false
}

// First provides a first non-empty range, while cleaning up empty ones.
func (rs *ranges[H]) First() (*headerRange[H], bool) {
	rs.lk.Lock()
	defer rs.lk.Unlock()

	for {
		if len(rs.ranges) == 0 {
			return nil, false
		}

		out := rs.ranges[0]
		if !out.Empty() {
			return out, true
		}

		rs.ranges = rs.ranges[1:]
	}
}

type headerRange[H header.Header] struct {
	lk      sync.RWMutex
	headers []H
	start   uint64
}

func newRange[H header.Header](h H) *headerRange[H] {
	return &headerRange[H]{
		start:   uint64(h.Height()),
		headers: []H{h},
	}
}

// Append appends new headers.
func (r *headerRange[H]) Append(h ...H) {
	r.lk.Lock()
	r.headers = append(r.headers, h...)
	r.lk.Unlock()
}

// Empty reports if range is empty.
func (r *headerRange[H]) Empty() bool {
	r.lk.RLock()
	defer r.lk.RUnlock()
	return len(r.headers) == 0
}

// Head reports the head of range if any.
func (r *headerRange[H]) Head() H {
	r.lk.RLock()
	defer r.lk.RUnlock()
	ln := len(r.headers)
	if ln == 0 {
		var zero H
		return zero
	}
	return r.headers[ln-1]
}

// Before truncates all the headers before height 'end' - [r.Start:end]
func (r *headerRange[H]) Before(end uint64) ([]H, uint64) {
	r.lk.Lock()
	defer r.lk.Unlock()

	amnt := uint64(len(r.headers))
	if r.start+amnt >= end {
		amnt = end - r.start + 1 // + 1 to include 'end' as well
	}

	out := r.headers[:amnt]
	r.headers = r.headers[amnt:]
	if len(r.headers) != 0 {
		r.start = uint64(r.headers[0].Height())
	}
	return out, amnt
}
