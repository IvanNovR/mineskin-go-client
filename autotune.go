package mineskin

import (
	"log/slog"
	"sync"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
)

// Auto-tuning constants (D10, D11).
const (
	autoBaselineInterval    = time.Second
	autoBaselineConcurrency = 1
	autoMinInterval         = 100 * time.Millisecond
	autoMaxInterval         = time.Second
	autoMinConcurrency      = 1
	autoMaxConcurrency      = 30
	grantsRefreshAge        = 5 * time.Minute
	penaltyStep             = 500 * time.Millisecond
	penaltyMax              = 10 * time.Second
	penaltyDecayPerSecond   = 100 * time.Millisecond
	maxEffectiveInterval    = 10 * time.Second

	// penaltyDecayDivisor turns elapsed time into decayed penalty: the
	// penalty falls by penaltyDecayPerSecond for each elapsed second.
	penaltyDecayDivisor = time.Second / penaltyDecayPerSecond
)

// tuner holds the generate group's limits when auto-tuning is on: the limits
// mapped from the user's grants plus a decaying failure penalty. It is safe
// for concurrent use.
//
// Lock order: the throttle calls limits while holding its own mutex, so the
// tuner never calls into the throttle (kick) while holding mu.
type tuner struct {
	clock  clock.Clock
	logger *slog.Logger
	// kick re-runs the throttle dispatch after the limits changed.
	kick func()

	mu          sync.Mutex
	interval    time.Duration
	concurrency int
	penalty     time.Duration
	penaltyAt   time.Time
}

// newTuner returns a tuner on the baseline limits. A nil logger discards logs.
func newTuner(c clock.Clock, l *slog.Logger) *tuner {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	return &tuner{
		clock:       c,
		logger:      l,
		interval:    autoBaselineInterval,
		concurrency: autoBaselineConcurrency,
	}
}

// limits returns the effective generate limits at now: the grants interval
// plus the decayed penalty, capped at maxEffectiveInterval.
func (t *tuner) limits(now time.Time) throttle.Limits {
	t.mu.Lock()
	defer t.mu.Unlock()
	return throttle.Limits{
		Interval:    min(t.interval+t.decayedLocked(now), maxEffectiveInterval),
		Concurrency: t.concurrency,
	}
}

// applyGrants maps grants onto the limits. A missing, zero or negative
// per-minute value keeps the current interval and a missing concurrency keeps
// the current concurrency.
func (t *tuner) applyGrants(g Grants) {
	t.mu.Lock()
	if g.PerMinute != nil && *g.PerMinute > 0 {
		t.interval = clampDuration((time.Minute / time.Duration(*g.PerMinute)).Truncate(time.Millisecond),
			autoMinInterval, autoMaxInterval)
	}
	if g.Concurrency != nil {
		t.concurrency = min(max(*g.Concurrency, autoMinConcurrency), autoMaxConcurrency)
	}
	interval, concurrency := t.interval, t.concurrency
	t.mu.Unlock()
	t.logger.Debug("MineSkin grants applied to the generate throttle",
		"interval", interval, "concurrency", concurrency)
	t.kickThrottle()
}

// reportFailure adds a penalty step on top of the decayed penalty.
func (t *tuner) reportFailure() {
	now := t.clock.Now()
	t.mu.Lock()
	t.penalty = min(t.decayedLocked(now)+penaltyStep, penaltyMax)
	t.penaltyAt = now
	penalty := t.penalty
	t.mu.Unlock()
	t.logger.Debug("MineSkin generate failure penalty applied", "penalty", penalty)
	t.kickThrottle()
}

// decayedLocked returns the penalty left at now. The elapsed time is compared
// with the full decay time before any multiplication, so nothing overflows.
func (t *tuner) decayedLocked(now time.Time) time.Duration {
	if t.penalty <= 0 {
		return 0
	}
	elapsed := now.Sub(t.penaltyAt)
	if elapsed < 0 {
		return t.penalty
	}
	if elapsed >= t.penalty*penaltyDecayDivisor {
		return 0
	}
	return t.penalty - elapsed/penaltyDecayDivisor
}

// kickThrottle re-runs the throttle dispatch. It must be called without mu.
func (t *tuner) kickThrottle() {
	if t.kick != nil {
		t.kick()
	}
}

// clampDuration limits d to [lo, hi].
func clampDuration(d, lo, hi time.Duration) time.Duration {
	return min(max(d, lo), hi)
}
