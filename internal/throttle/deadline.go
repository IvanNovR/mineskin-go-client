// Package throttle paces outgoing requests: a FIFO wait queue with a real
// in-flight cap, a minimum interval between starts and a "not before"
// deadline.
package throttle

import (
	"sync/atomic"
	"time"
)

// Deadline is a goroutine-safe "not before" instant that only ever moves
// later. The zero value is unset. It must not be copied after first use.
type Deadline struct {
	ns atomic.Int64 // unix nanoseconds; 0 means unset
}

// Extend moves the deadline to t if t is later than the current value, in a
// single compare-and-swap loop, and reports whether it moved. A zero t is
// ignored.
func (d *Deadline) Extend(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	want := t.UnixNano()
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
