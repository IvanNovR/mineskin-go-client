package mineskin

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

var tunerT0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newTestTuner(t *testing.T) (*tuner, *clock.Fake) {
	t.Helper()
	fake := clock.NewFake(tunerT0)
	return newTuner(fake, nil), fake
}

func intPtr(n int) *int { return &n }

func TestTunerBaselineBeforeLoad(t *testing.T) {
	tn, fake := newTestTuner(t)
	got := tn.limits(fake.Now())
	if got.Interval != time.Second || got.Concurrency != 1 {
		t.Errorf("limits = %+v, want 1s/1 (JB-01)", got)
	}
}

func TestTunerApplyGrants(t *testing.T) {
	tests := []struct {
		name         string
		grants       Grants
		wantInterval time.Duration
		wantConc     int
	}{
		{"60 per minute", Grants{PerMinute: intPtr(60)}, time.Second, 1},
		{"600 per minute", Grants{PerMinute: intPtr(600)}, 100 * time.Millisecond, 1},
		{"6000 clamps to min", Grants{PerMinute: intPtr(6000)}, 100 * time.Millisecond, 1},
		{"30 clamps to max", Grants{PerMinute: intPtr(30)}, time.Second, 1},
		{"70 truncates to ms", Grants{PerMinute: intPtr(70)}, 857 * time.Millisecond, 1},
		{"per minute missing keeps interval", Grants{}, time.Second, 1},
		{"per minute zero keeps interval", Grants{PerMinute: intPtr(0)}, time.Second, 1},
		{"per minute negative keeps interval", Grants{PerMinute: intPtr(-4)}, time.Second, 1},
		{"concurrency 10", Grants{Concurrency: intPtr(10)}, time.Second, 10},
		{"concurrency 0 clamps to 1", Grants{Concurrency: intPtr(0)}, time.Second, 1},
		{"concurrency negative clamps to 1", Grants{Concurrency: intPtr(-5)}, time.Second, 1},
		{"concurrency 50 clamps to 30", Grants{Concurrency: intPtr(50)}, time.Second, 30},
		{"concurrency missing keeps value", Grants{PerMinute: intPtr(600)}, 100 * time.Millisecond, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tn, fake := newTestTuner(t)
			tn.applyGrants(tt.grants)
			got := tn.limits(fake.Now())
			if got.Interval != tt.wantInterval || got.Concurrency != tt.wantConc {
				t.Errorf("limits = %+v, want %v/%d", got, tt.wantInterval, tt.wantConc)
			}
		})
	}
}

func TestTunerApplyGrantsKeepsUnsetFieldsFromEarlierGrants(t *testing.T) {
	tn, fake := newTestTuner(t)
	tn.applyGrants(Grants{PerMinute: intPtr(600), Concurrency: intPtr(7)})
	tn.applyGrants(Grants{})
	got := tn.limits(fake.Now())
	if got.Interval != 100*time.Millisecond || got.Concurrency != 7 {
		t.Errorf("limits = %+v, want 100ms/7", got)
	}
}

func TestTunerKeepsLastKnownValuesWithoutNewGrants(t *testing.T) {
	tn, fake := newTestTuner(t)
	tn.applyGrants(Grants{PerMinute: intPtr(300), Concurrency: intPtr(4)})
	fake.Advance(time.Hour)
	got := tn.limits(fake.Now())
	if got.Interval != 200*time.Millisecond || got.Concurrency != 4 {
		t.Errorf("limits = %+v, want 200ms/4", got)
	}
}

func TestTunerPenaltyAddsStep(t *testing.T) {
	tn, fake := newTestTuner(t)
	tn.applyGrants(Grants{PerMinute: intPtr(60)})
	tn.reportFailure()
	if got := tn.limits(fake.Now()).Interval; got != 1500*time.Millisecond {
		t.Errorf("after one failure interval = %v, want 1.5s", got)
	}
	tn.reportFailure()
	if got := tn.limits(fake.Now()).Interval; got != 2*time.Second {
		t.Errorf("after two failures interval = %v, want 2s", got)
	}
}

func TestTunerPenaltyDecaysLinearly(t *testing.T) {
	tn, fake := newTestTuner(t)
	tn.applyGrants(Grants{PerMinute: intPtr(60)})
	tn.reportFailure()
	fake.Advance(2 * time.Second)
	if got := tn.limits(fake.Now()).Interval; got != 1300*time.Millisecond {
		t.Errorf("after 2s interval = %v, want 1.3s", got)
	}
	fake.Advance(10 * time.Second)
	if got := tn.limits(fake.Now()).Interval; got != time.Second {
		t.Errorf("after 12s interval = %v, want 1s (floored)", got)
	}
}

func TestTunerPenaltyStacksOnDecayed(t *testing.T) {
	tn, fake := newTestTuner(t)
	tn.applyGrants(Grants{PerMinute: intPtr(60)})
	tn.reportFailure()
	fake.Advance(3 * time.Second)
	tn.reportFailure()
	if got := tn.limits(fake.Now()).Interval; got != 1700*time.Millisecond {
		t.Errorf("interval = %v, want 1.7s", got)
	}
}

func TestTunerPenaltyClamp(t *testing.T) {
	tn, fake := newTestTuner(t)
	tn.applyGrants(Grants{PerMinute: intPtr(60)})
	for range 30 {
		tn.reportFailure()
	}
	if got := tn.limits(fake.Now()).Interval; got != 10*time.Second {
		t.Errorf("interval = %v, want 10s (capped)", got)
	}
	tn.mu.Lock()
	defer tn.mu.Unlock()
	if tn.penalty != 10*time.Second {
		t.Errorf("penalty = %v, want 10s", tn.penalty)
	}
}

func TestTunerDecayNoOverflow(t *testing.T) {
	tn, fake := newTestTuner(t)
	tn.applyGrants(Grants{PerMinute: intPtr(60)})
	tn.reportFailure()
	fake.Advance(1000 * time.Hour)
	if got := tn.limits(fake.Now()).Interval; got != time.Second {
		t.Errorf("interval = %v, want 1s after 1000h", got)
	}
	// A clock that went backwards must not yield a larger penalty.
	if got := tn.limits(fake.Now().Add(-2000 * time.Hour)).Interval; got < time.Second || got > 10*time.Second {
		t.Errorf("interval = %v with a backwards clock, want within [1s, 10s]", got)
	}
}

func TestTunerRealClock(t *testing.T) {
	tn := newTuner(clock.Real(), nil)
	tn.applyGrants(Grants{PerMinute: intPtr(60)})
	tn.reportFailure()
	got := tn.limits(time.Now()).Interval
	if got <= time.Second || got > 1500*time.Millisecond {
		t.Errorf("interval = %v, want in (1s, 1.5s]", got)
	}
}

func TestTunerCallsKickOutsideLock(t *testing.T) {
	tn, _ := newTestTuner(t)
	kicks := 0
	tn.kick = func() {
		if !tn.mu.TryLock() {
			t.Error("kick called with tuner mutex held")
			return
		}
		tn.mu.Unlock()
		kicks++
	}
	tn.applyGrants(Grants{PerMinute: intPtr(600)})
	tn.reportFailure()
	if kicks != 2 {
		t.Errorf("kicks = %d, want 2", kicks)
	}
}

func TestTunerLogsChangesAtDebug(t *testing.T) {
	var buf bytes.Buffer
	tn := newTuner(clock.NewFake(tunerT0), captureLogger(&buf))
	tn.applyGrants(Grants{PerMinute: intPtr(600), Concurrency: intPtr(3)})
	tn.reportFailure()
	out := buf.String()
	for _, want := range []string{"level=DEBUG", "grants", "penalty"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
}
