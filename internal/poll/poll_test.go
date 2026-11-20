package poll

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func waitingFetcher() *fakeFetcher {
	return &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return waiting(id), nil }}
}

func TestIntervalError_Messages(t *testing.T) {
	if got := (&IntervalError{Value: -time.Second}).Error(); got != "poll interval -1s is not greater than zero" {
		t.Errorf("value error = %q", got)
	}
	if got := (&IntervalError{Panic: "boom"}).Error(); got != "poll interval panicked: boom" {
		t.Errorf("panic error = %q", got)
	}
	if got := (&ExhaustedError{ID: "j", Attempts: 3}).Error(); got != "job j not finished after 3 attempts" {
		t.Errorf("exhausted error = %q", got)
	}
}

func TestPoller_IntervalCallbackRunsOutsideLock(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	p := newTestPoller(t, waitingFetcher(), Options{Interval: func(int) time.Duration {
		close(entered)
		<-release
		return time.Second
	}})
	p.add("a", time.Time{})
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		p.tick(pollerT0.Add(time.Second))
	}()
	<-entered

	added := make(chan struct{})
	go func() {
		defer close(added)
		p.add("b", time.Time{})
	}()
	select {
	case <-added:
	case <-time.After(2 * time.Second):
		t.Error("add blocked while the interval callback ran: lock held")
	}
	close(release)
	<-ticked
}

func TestPoller_CancelledJobIsNotRescheduled(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	p := newTestPoller(t, waitingFetcher(), Options{Interval: func(int) time.Duration {
		close(entered)
		<-release
		return time.Second
	}})
	a := p.add("a", time.Time{})
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		p.tick(pollerT0.Add(time.Second))
	}()
	<-entered
	p.Leave(a)
	close(release)
	<-ticked

	if gets, _ := p.fetch.(*fakeFetcher).counts(); len(gets) != 0 {
		t.Errorf("Get calls = %v, want none for a job that left", gets)
	}
}

func TestPoller_SkipsJobsThatLeftDuringTick(t *testing.T) {
	var p *Poller[*fakeResp]
	var ws []*Waiter[*fakeResp]
	f := &fakeFetcher{slot: &sync.Mutex{}}
	f.getFn = func(id string) (*fakeResp, error) {
		for _, w := range ws {
			if w.id != id {
				p.Leave(w) // the only waiter of the other due job cancels
			}
		}
		return waiting(id), nil
	}
	p = newTestPoller(t, f, Options{})
	ws = addJobs(p, 2)

	p.tick(pollerT0.Add(time.Second))

	if gets, _ := f.counts(); len(gets) != 1 {
		t.Errorf("Get calls = %v, want exactly 1 (job that left must be skipped)", gets)
	}
}

func TestOptions_FirstDelay(t *testing.T) {
	now := pollerT0
	tests := []struct {
		name string
		opts Options
		eta  time.Time
		want time.Duration
	}{
		{"UseETA off ignores future ETA", Options{InitialDelay: 2 * time.Second}, now.Add(9 * time.Second), 2 * time.Second},
		{"future ETA", Options{InitialDelay: 2 * time.Second, UseETA: true}, now.Add(9 * time.Second), 9 * time.Second},
		{"ETA ten years ahead is capped", Options{InitialDelay: 2 * time.Second, UseETA: true}, now.Add(10 * 365 * 24 * time.Hour), time.Hour},
		{"ETA just under the cap is kept", Options{InitialDelay: 2 * time.Second, UseETA: true}, now.Add(time.Hour - time.Second), time.Hour - time.Second},
		{"past ETA", Options{InitialDelay: 2 * time.Second, UseETA: true}, now.Add(-time.Second), 2 * time.Second},
		{"ETA equal now", Options{InitialDelay: 2 * time.Second, UseETA: true}, now, 2 * time.Second},
		{"zero ETA", Options{InitialDelay: 2 * time.Second, UseETA: true}, time.Time{}, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.opts.firstDelay(tt.eta, now); got != tt.want {
				t.Errorf("firstDelay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOptions_IntervalResults(t *testing.T) {
	neg := Options{Interval: func(int) time.Duration { return -time.Millisecond }}
	var ie *IntervalError
	if _, err := neg.interval(1); !errors.As(err, &ie) || ie.Value != -time.Millisecond {
		t.Errorf("err = %v, want *IntervalError", err)
	}
	ok := Options{Interval: func(a int) time.Duration { return time.Duration(a) * time.Millisecond }}
	if got, err := ok.interval(3); err != nil || got != 3*time.Millisecond {
		t.Errorf("got (%v, %v)", got, err)
	}
}

func TestErrSkipped_IsAComparableSentinel(t *testing.T) {
	if !errors.Is(ErrSkipped, ErrSkipped) || ErrSkipped.Error() == "" {
		t.Errorf("ErrSkipped = %q", ErrSkipped)
	}
}

func TestPoller_IntervalFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// interval is consulted once per attempt; badAttempt is the attempt on
		// which it misbehaves.
		interval     func(attempt int) time.Duration
		badAttempt   int
		wantValue    time.Duration
		wantPanic    any
		wantGetCalls []string
	}{
		{
			name:       "zero interval",
			interval:   func(int) time.Duration { return 0 },
			badAttempt: 1, wantValue: 0, wantGetCalls: nil,
		},
		{
			name: "negative interval",
			interval: func(attempt int) time.Duration {
				if attempt == 2 {
					return -time.Millisecond
				}
				return time.Second
			},
			badAttempt: 2, wantValue: -time.Millisecond, wantGetCalls: []string{"a", "b"},
		},
		{
			name: "panicking interval",
			interval: func(attempt int) time.Duration {
				if attempt == 2 {
					panic("boom")
				}
				return time.Second
			},
			badAttempt: 2, wantPanic: "boom", wantGetCalls: []string{"a", "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := waitingFetcher()
			p := newTestPoller(t, f, Options{Interval: tt.interval})
			a := p.add("a", time.Time{})
			var b *Waiter[*fakeResp]
			if tt.badAttempt == 2 {
				p.tick(pollerT0.Add(time.Second)) // a: attempt 1
				b = p.add("b", time.Time{})
				p.mu.Lock()
				b.nextCheckAt = pollerT0.Add(2 * time.Second)
				p.mu.Unlock()
				p.tick(pollerT0.Add(2 * time.Second)) // a: bad attempt 2, b: attempt 1
			} else {
				p.tick(pollerT0.Add(time.Second))
			}

			var ie *IntervalError
			if !isFinished(a) || !errors.As(a.err, &ie) || ie.Value != tt.wantValue || ie.Panic != tt.wantPanic {
				t.Errorf("a err = %v, want *IntervalError{Value: %v, Panic: %v}", a.err, tt.wantValue, tt.wantPanic)
			}
			if b != nil && isFinished(b) {
				t.Error("b finished, want still pending (only the misbehaving job fails)")
			}
			gets, _ := f.counts()
			if len(gets) != len(tt.wantGetCalls) {
				t.Fatalf("Get calls = %v, want %v", gets, tt.wantGetCalls)
			}
			for i := range gets {
				if gets[i] != tt.wantGetCalls[i] {
					t.Errorf("Get calls = %v, want %v", gets, tt.wantGetCalls)
					break
				}
			}
		})
	}
}
