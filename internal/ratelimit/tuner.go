package ratelimit

import (
	"context"
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

// Tuner holds the generate group's limits when auto-tuning is on: the limits
// mapped from the user's grants plus a decaying failure penalty. It is safe
// for concurrent use.
//
// Lock order: the throttle calls limits while holding its own mutex, so the
// tuner never calls into the throttle (kick) while holding mu.
type Tuner struct {
	clock  clock.Clock
	logger *slog.Logger
	// kick re-runs the throttle dispatch after the limits changed. It is nil
	// only until SetKick is called.
	kick func()

	mu          sync.Mutex
	interval    time.Duration
	concurrency int
	penalty     time.Duration
	penaltyAt   time.Time

	grants grantsLoad
}

// Grants are the limits the API key is granted. A nil field means the API did
// not send a usable value.
type Grants struct {
	PerMinute   *int
	Concurrency *int
}

// TunerConfig is everything a Tuner needs, except the throttle kick, which is
// set afterwards with SetKick because the throttle needs the tuner's Limits.
type TunerConfig struct {
	Clock  clock.Clock
	Logger *slog.Logger // nil discards logs
	// Load fetches the current grants. It runs on BaseCtx.
	Load func(ctx context.Context) (Grants, error)
	// BaseCtx parents every load; its cancellation ends loads and silences
	// their failure logs.
	BaseCtx context.Context
	// BG tracks the load goroutines so the owner can wait for them.
	BG *sync.WaitGroup
}

// NewTuner returns a Tuner on the baseline limits.
func NewTuner(cfg TunerConfig) *Tuner {
	l := cfg.Logger
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	return &Tuner{
		clock:       cfg.Clock,
		logger:      l,
		interval:    autoBaselineInterval,
		concurrency: autoBaselineConcurrency,
		grants:      grantsLoad{load: cfg.Load, baseCtx: cfg.BaseCtx, bg: cfg.BG},
	}
}

// SetKick sets the function that re-runs the throttle dispatch after the
// limits changed (the throttle's Kick). It must be called once, before the
// tuner is used.
func (t *Tuner) SetKick(kick func()) { t.kick = kick }

// Limits returns the effective generate limits at now: the grants interval
// plus the decayed penalty, capped at maxEffectiveInterval.
func (t *Tuner) Limits(now time.Time) throttle.Limits {
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
func (t *Tuner) applyGrants(g Grants) {
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

// ReportFailure adds a penalty step on top of the decayed penalty.
func (t *Tuner) ReportFailure() {
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
func (t *Tuner) decayedLocked(now time.Time) time.Duration {
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
func (t *Tuner) kickThrottle() {
	if t.kick != nil {
		t.kick()
	}
}

// clampDuration limits d to [lo, hi].
func clampDuration(d, lo, hi time.Duration) time.Duration {
	return min(max(d, lo), hi)
}
