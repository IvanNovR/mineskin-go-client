package clock

import (
	"sync/atomic"
	"testing"
	"time"
)

const realTimerWait = 5 * time.Second

func TestRealClockAfterFuncFiresAndStops(t *testing.T) {
	c := Real()

	fired := make(chan struct{})
	c.AfterFunc(time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(realTimerWait):
		t.Fatal("real timer did not fire")
	}

	var ran atomic.Bool
	timer := c.AfterFunc(time.Hour, func() { ran.Store(true) })
	if !timer.Stop() {
		t.Fatal("Stop() = false for a pending timer")
	}
	if ran.Load() {
		t.Fatal("stopped timer ran")
	}
}

func TestRealClockNowIsCurrent(t *testing.T) {
	before := time.Now()
	got := Real().Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want between %v and %v", got, before, after)
	}
}
