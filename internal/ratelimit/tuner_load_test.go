package ratelimit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

const staleRefresh = grantsRefreshAge + 2*time.Second

// fakeLoader counts calls and lets a test decide each result.
type fakeLoader struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, call int) (Grants, error)
}

func (f *fakeLoader) load(ctx context.Context) (Grants, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	return f.fn(ctx, n)
}

func (f *fakeLoader) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func grantsOf(perMinute, concurrency int) Grants {
	return Grants{PerMinute: &perMinute, Concurrency: &concurrency}
}

type loaderEnv struct {
	tn     *Tuner
	fake   *clock.Fake
	cancel context.CancelFunc
	bg     *sync.WaitGroup
}

func newLoaderEnv(t *testing.T, f *fakeLoader, logger *slog.Logger) *loaderEnv {
	t.Helper()
	fake := clock.NewFake(tunerT0)
	base, cancel := context.WithCancel(context.Background())
	bg := new(sync.WaitGroup)
	tn := NewTuner(TunerConfig{Clock: fake, Logger: logger, Load: f.load, BaseCtx: base, BG: bg})
	tn.SetKick(func() {})
	t.Cleanup(func() { cancel(); bg.Wait() })
	return &loaderEnv{tn: tn, fake: fake, cancel: cancel, bg: bg}
}

func waitLoadIdle(t *testing.T, tn *Tuner) {
	t.Helper()
	eventually(t, "grants load to finish", func() bool {
		tn.mu.Lock()
		defer tn.mu.Unlock()
		return !tn.grants.loading
	})
}

func limitsOf(e *loaderEnv) (time.Duration, int) {
	l := e.tn.Limits(e.fake.Now())
	return l.Interval, l.Concurrency
}

func TestEnsureLoadedAppliesGrantsAndIsDoneAfterwards(t *testing.T) {
	f := &fakeLoader{fn: func(context.Context, int) (Grants, error) { return grantsOf(600, 3), nil }}
	e := newLoaderEnv(t, f, nil)
	if e.tn.Loaded() {
		t.Fatal("Loaded() before any load")
	}
	if err := e.tn.ensureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.tn.ensureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if iv, n := limitsOf(e); iv != 100*time.Millisecond || n != 3 || !e.tn.Loaded() || f.count() != 1 {
		t.Errorf("limits = %v/%d, loaded = %v, loads = %d; want 100ms/3, true, 1", iv, n, e.tn.Loaded(), f.count())
	}
}

func TestEnsureLoadedConcurrentCallersShareOneLoad(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeLoader{fn: func(ctx context.Context, _ int) (Grants, error) {
		<-gate
		return grantsOf(600, 3), nil
	}}
	e := newLoaderEnv(t, f, nil)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if err := e.tn.ensureLoaded(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	eventually(t, "load to start", func() bool { return f.count() == 1 })
	close(gate)
	wg.Wait()
	if got := f.count(); got != 1 {
		t.Errorf("loads = %d, want 1", got)
	}
}

func TestEnsureLoadedFailureUsesBaselineAndWarnsOnce(t *testing.T) {
	var logs bytes.Buffer
	f := &fakeLoader{fn: func(context.Context, int) (Grants, error) { return Grants{}, errors.New("bad key") }}
	e := newLoaderEnv(t, f, captureLogger(&logs))
	if err := e.tn.ensureLoaded(context.Background()); err != nil {
		t.Fatalf("ensureLoaded() error = %v, want nil after a failed load", err)
	}
	if iv, n := limitsOf(e); iv != time.Second || n != 1 {
		t.Errorf("limits = %v/%d, want baseline 1s/1", iv, n)
	}
	if got := strings.Count(logs.String(), "level=WARN msg=\"Loading MineSkin grants failed"); got != 1 {
		t.Errorf("warnings = %d, want 1; log:\n%s", got, logs.String())
	}
}

func TestEnsureLoadedCallerCancelKeepsTheLoad(t *testing.T) {
	release := make(chan struct{})
	f := &fakeLoader{fn: func(ctx context.Context, _ int) (Grants, error) {
		select {
		case <-release:
			return grantsOf(600, 3), nil
		case <-ctx.Done():
			return Grants{}, ctx.Err()
		}
	}}
	e := newLoaderEnv(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- e.tn.ensureLoaded(ctx) }()
	eventually(t, "load to start", func() bool { return f.count() == 1 })
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	close(release)
	if err := e.tn.ensureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if iv, n := limitsOf(e); f.count() != 1 || iv != 100*time.Millisecond || n != 3 {
		t.Errorf("loads = %d, limits = %v/%d; want 1 load surviving the cancel, 100ms/3", f.count(), iv, n)
	}
}

func TestEnsureLoadedAfterShutdownIsErrClosed(t *testing.T) {
	f := &fakeLoader{fn: func(context.Context, int) (Grants, error) { return Grants{}, nil }}
	e := newLoaderEnv(t, f, nil)
	e.tn.Shutdown()
	if err := e.tn.ensureLoaded(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("error = %v, want ErrClosed", err)
	}
	if f.count() != 0 {
		t.Error("a load started after Shutdown")
	}
}

func TestMaybeRefreshAfterFiveMinutes(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeLoader{fn: func(ctx context.Context, call int) (Grants, error) {
		if call == 1 {
			return grantsOf(600, 3), nil
		}
		select {
		case <-gate:
			return grantsOf(60, 2), nil
		case <-ctx.Done():
			return Grants{}, ctx.Err()
		}
	}}
	e := newLoaderEnv(t, f, nil)
	if err := e.tn.ensureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.fake.Advance(grantsRefreshAge - time.Second)
	e.tn.maybeRefresh()
	if got := f.count(); got != 1 {
		t.Fatalf("loads before the age = %d, want 1", got)
	}
	e.fake.Advance(2 * time.Second)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(e.tn.maybeRefresh)
	}
	wg.Wait()
	eventually(t, "refresh to start", func() bool { return f.count() == 2 })
	if got := f.count(); got != 2 {
		t.Errorf("loads = %d, want exactly one refresh", got)
	}
	close(gate)
	waitLoadIdle(t, e.tn)
	if iv, n := limitsOf(e); iv != time.Second || n != 2 {
		t.Errorf("limits = %v/%d, want 1s/2 from the refreshed grants", iv, n)
	}
}

func TestMaybeRefreshFailureKeepsGrantsAndRetriesAfterFiveMinutes(t *testing.T) {
	var logs bytes.Buffer
	f := &fakeLoader{fn: func(_ context.Context, call int) (Grants, error) {
		if call == 1 {
			return grantsOf(600, 3), nil
		}
		return Grants{}, errors.New("boom")
	}}
	e := newLoaderEnv(t, f, captureLogger(&logs))
	if err := e.tn.ensureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.fake.Advance(staleRefresh)
	e.tn.maybeRefresh()
	eventually(t, "refresh attempt", func() bool { return f.count() == 2 })
	waitLoadIdle(t, e.tn)
	if iv, n := limitsOf(e); iv != 100*time.Millisecond || n != 3 {
		t.Errorf("limits = %v/%d, want last-known 100ms/3", iv, n)
	}
	if got := strings.Count(logs.String(), "level=WARN msg=\"Refreshing MineSkin grants failed"); got != 1 {
		t.Errorf("refresh warnings = %d, want 1; log:\n%s", got, logs.String())
	}
	// D21: the next try is 5 minutes after the failed attempt, not sooner.
	e.fake.Advance(time.Minute)
	e.tn.maybeRefresh()
	if got := f.count(); got != 2 {
		t.Errorf("loads 1 min after a failure = %d, want 2", got)
	}
	e.fake.Advance(grantsRefreshAge)
	e.tn.maybeRefresh()
	eventually(t, "retry after 5 minutes", func() bool { return f.count() == 3 })
}

func TestBaseCtxCancelEndsLoadAndBGWaitsForIt(t *testing.T) {
	var exited atomic.Bool
	cancelled := make(chan struct{})
	finish := make(chan struct{})
	var logs bytes.Buffer
	f := &fakeLoader{fn: func(ctx context.Context, _ int) (Grants, error) {
		<-ctx.Done()
		close(cancelled)
		<-finish
		exited.Store(true)
		return Grants{}, ctx.Err()
	}}
	e := newLoaderEnv(t, f, captureLogger(&logs))
	go func() { _ = e.tn.ensureLoaded(context.Background()) }()
	eventually(t, "load to start", func() bool { return f.count() == 1 })
	e.tn.Shutdown()
	e.cancel()
	<-cancelled
	waited := make(chan struct{})
	go func() { e.bg.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("BG.Wait returned while the loader was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	<-waited
	if !exited.Load() {
		t.Error("BG.Wait returned before the loader exited")
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a failure caused by cancellation was logged:\n%s", logs.String())
	}
}

// rawAttrHandler keeps attribute values unescaped, like a custom handler that
// writes them straight to a terminal.
type rawAttrHandler struct {
	mu    sync.Mutex
	attrs []string
}

func (h *rawAttrHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *rawAttrHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *rawAttrHandler) WithGroup(string) slog.Handler            { return h }
func (h *rawAttrHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Level < slog.LevelWarn {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		h.attrs = append(h.attrs, a.Value.String())
		return true
	})
	return nil
}

func TestLoadErrorLogStripsControlCharacters(t *testing.T) {
	h := &rawAttrHandler{}
	f := &fakeLoader{fn: func(context.Context, int) (Grants, error) {
		return Grants{}, errors.New("bad\x1b[2J \x1b[31mred\x1b[0m\r\nforged line\x07")
	}}
	e := newLoaderEnv(t, f, slog.New(h))
	if err := e.tn.ensureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	logged := strings.Join(h.attrs, "|")
	if !strings.Contains(logged, "red") {
		t.Fatalf("logged error lost its text: %q", logged)
	}
	for _, r := range logged {
		if unicode.IsControl(r) {
			t.Fatalf("logged error contains control character %U: %q", r, logged)
		}
	}
}
