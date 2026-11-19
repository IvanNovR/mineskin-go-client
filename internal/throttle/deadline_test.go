package throttle

import (
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const concurrentExtenders = 64 // goroutines extending at once

func TestDeadlineZeroIsUnset(t *testing.T) {
	var d Deadline

	if got := d.Load(); !got.IsZero() {
		t.Fatalf("Load() = %v, want the zero time", got)
	}
}

func TestDeadlineExtendOnlyMovesLater(t *testing.T) {
	var d Deadline
	steps := []struct {
		name      string
		to        time.Duration
		wantMoved bool
		wantAt    time.Duration
	}{
		{"first extend from unset", 0, true, 0},
		{"later", 5 * time.Second, true, 5 * time.Second},
		{"earlier is ignored", 2 * time.Second, false, 5 * time.Second},
		{"equal is ignored", 5 * time.Second, false, 5 * time.Second},
		{"later again", 9 * time.Second, true, 9 * time.Second},
	}

	for _, s := range steps {
		moved := d.Extend(t0.Add(s.to))

		if moved != s.wantMoved {
			t.Errorf("%s: Extend = %v, want %v", s.name, moved, s.wantMoved)
		}
		if got, want := d.Load(), t0.Add(s.wantAt); !got.Equal(want) {
			t.Errorf("%s: Load = %v, want %v", s.name, got, want)
		}
	}
}

func TestDeadlineExtendIgnoresZeroTime(t *testing.T) {
	var d Deadline
	d.Extend(t0)

	if d.Extend(time.Time{}) {
		t.Fatal("Extend(zero time) moved the deadline")
	}
	if !d.Load().Equal(t0) {
		t.Fatalf("Load = %v, want %v", d.Load(), t0)
	}
}

func TestDeadlineConcurrentExtendKeepsMax(t *testing.T) {
	var d Deadline
	offsets := make([]time.Duration, concurrentExtenders)
	var maxOffset time.Duration
	for i := range offsets {
		offsets[i] = time.Duration((i*7919)%1009+1) * time.Second // scrambled, deterministic
		maxOffset = max(maxOffset, offsets[i])
	}

	var wg sync.WaitGroup
	for _, off := range offsets {
		wg.Go(func() { d.Extend(t0.Add(off)) })
	}
	wg.Wait()

	if got, want := d.Load(), t0.Add(maxOffset); !got.Equal(want) {
		t.Fatalf("Load = %v, want max %v", got, want)
	}
}

func TestDeadlineExtendSaturatesFarFutureTimes(t *testing.T) {
	var d Deadline
	farFuture := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	d.Extend(t0)

	if !d.Extend(farFuture) {
		t.Fatal("Extend(year 3000) = false, want it to move the deadline")
	}
	if got := d.Load(); !got.After(t0.Add(100 * 365 * 24 * time.Hour)) {
		t.Errorf("Load() = %v, want a far-future saturated deadline", got)
	}
	if d.Extend(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("a smaller time moved a saturated deadline")
	}
	if d.Extend(time.Date(4000, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("a larger far-future time moved an already saturated deadline")
	}
}
