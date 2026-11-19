// Package throttle paces outgoing requests: a FIFO wait queue with a real
// in-flight cap, a minimum interval between starts and a "not before"
// deadline.
package throttle

import (
	"math"
	"sync/atomic"
	"time"
)

// maxDeadline is the last instant a Deadline can store.
var maxDeadline = time.Unix(0, math.MaxInt64)

// Deadline is a goroutine-safe "not before" instant that only ever moves
// later. The zero value is unset. It must not be copied after first use.
type Deadline struct {
	ns atomic.Int64 // unix nanoseconds; 0 means unset
}

// Extend moves the deadline to t if t is later than the current value, in a
// single compare-and-swap loop, and reports whether it moved. A zero t is
// ignored. A t past the last representable instant (about year 2262) is
// saturated to it, so a huge delay still holds at least as long as a smaller
// one instead of overflowing.
func (d *Deadline) Extend(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	want := int64(math.MaxInt64)
	if t.Before(maxDeadline) {
		want = t.UnixNano()
	}
	for {
		cur := d.ns.Load()
		if want <= cur {
			return false
		}
		if d.ns.CompareAndSwap(cur, want) {
			return true
		}
	}
}

// Load returns the current deadline, or the zero time if it is unset.
func (d *Deadline) Load() time.Time {
	ns := d.ns.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}
