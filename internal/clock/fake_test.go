package clock

import (
	"slices"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestFakeAdvanceFiresInOrder(t *testing.T) {
	// Arrange
	f := NewFake(t0)
	var mu sync.Mutex
	var got []int
	record := func(i int) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, i)
		}
	}
	f.AfterFunc(3*time.Second, record(3))
	f.AfterFunc(1*time.Second, record(1))
	f.AfterFunc(2*time.Second, record(2))

	// Act
	f.Advance(5 * time.Second)

	// Assert
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("fire order = %v, want [1 2 3]", got)
	}
	if want := t0.Add(5 * time.Second); !f.Now().Equal(want) {
		t.Fatalf("Now() = %v, want %v", f.Now(), want)
	}
}

func TestFakeNowDuringCallbackIsDueTime(t *testing.T) {
	f := NewFake(t0)
	var seen time.Time
	f.AfterFunc(2*time.Second, func() { seen = f.Now() })

	f.Advance(5 * time.Second)

	if want := t0.Add(2 * time.Second); !seen.Equal(want) {
		t.Fatalf("Now() in callback = %v, want %v", seen, want)
	}
}

func TestFakeEqualDeadlinesFireInCreationOrder(t *testing.T) {
	f := NewFake(t0)
	var got []int
	for i := range 4 {
		f.AfterFunc(time.Second, func() { got = append(got, i) })
	}

	f.Advance(time.Second)

	if !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Fatalf("order = %v, want [0 1 2 3]", got)
	}
}

func TestFakeAdvanceBeforeDueDoesNotFire(t *testing.T) {
	f := NewFake(t0)
	fired := false
	f.AfterFunc(time.Second, func() { fired = true })

	f.Advance(999 * time.Millisecond)

	if fired {
		t.Fatal("timer fired early")
	}
	f.Advance(time.Millisecond)
	if !fired {
		t.Fatal("timer did not fire at its due time")
	}
}

func TestFakeStopPreventsFire(t *testing.T) {
	f := NewFake(t0)
	fired := false
	timer := f.AfterFunc(time.Second, func() { fired = true })

	if !timer.Stop() {
		t.Fatal("Stop() = false for a pending timer")
	}
	if timer.Stop() {
		t.Fatal("second Stop() = true, want false")
	}
	f.Advance(time.Minute)

	if fired {
		t.Fatal("stopped timer fired")
	}
}

func TestFakeStopAfterFireReturnsFalse(t *testing.T) {
	f := NewFake(t0)
	timer := f.AfterFunc(time.Second, func() {})
	f.Advance(time.Second)

	if timer.Stop() {
		t.Fatal("Stop() = true after the timer fired")
	}
}

func TestFakeCallbackMayScheduleTimer(t *testing.T) {
	f := NewFake(t0)
	inner := false
	f.AfterFunc(time.Second, func() {
		f.AfterFunc(0, func() { inner = true })
	})

	done := make(chan struct{})
	go func() {
		f.Advance(time.Second)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Advance deadlocked when a callback scheduled a timer")
	}
	if !inner {
		t.Fatal("timer scheduled by a callback did not fire within the same Advance")
	}
}

func TestFakeCallbackTimerBeyondAdvanceWindowDoesNotFire(t *testing.T) {
	f := NewFake(t0)
	inner := false
	f.AfterFunc(time.Second, func() {
		f.AfterFunc(10*time.Second, func() { inner = true })
	})

	f.Advance(2 * time.Second)

	if inner {
		t.Fatal("timer beyond the window fired")
	}
	if f.Timers() != 1 {
		t.Fatalf("Timers() = %d, want 1", f.Timers())
	}
}

func TestFakeNonPositiveDelayFiresOnNextAdvance(t *testing.T) {
	f := NewFake(t0)
	fired := false
	f.AfterFunc(-time.Second, func() { fired = true })

	f.Advance(0)

	if !fired {
		t.Fatal("timer with a non-positive delay did not fire on Advance(0)")
	}
}

func TestFakeTimersCount(t *testing.T) {
	f := NewFake(t0)
	if f.Timers() != 0 {
		t.Fatalf("Timers() = %d, want 0", f.Timers())
	}
	a := f.AfterFunc(time.Second, func() {})
	f.AfterFunc(2*time.Second, func() {})
	if f.Timers() != 2 {
		t.Fatalf("Timers() = %d, want 2", f.Timers())
	}
	a.Stop()
	if f.Timers() != 1 {
		t.Fatalf("Timers() after Stop = %d, want 1", f.Timers())
	}
	f.Advance(2 * time.Second)
	if f.Timers() != 0 {
		t.Fatalf("Timers() after Advance = %d, want 0", f.Timers())
	}
}

func TestFakeImplementsClock(t *testing.T) {
	var _ Clock = NewFake(t0)
}
