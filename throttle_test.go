package mineskin

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestThrottleWithDefaults(t *testing.T) {
	def := Throttle{Interval: 200 * time.Millisecond, Concurrency: 1}
	tests := []struct {
		name    string
		in      Throttle
		want    Throttle
		wantErr bool
	}{
		{"zero value uses defaults", Throttle{}, def, false},
		{"interval only keeps default concurrency", Throttle{Interval: time.Second}, Throttle{Interval: time.Second, Concurrency: 1}, false},
		{"concurrency only keeps default interval", Throttle{Concurrency: 7}, Throttle{Interval: 200 * time.Millisecond, Concurrency: 7}, false},
		{"both set are kept", Throttle{Interval: time.Millisecond, Concurrency: 3}, Throttle{Interval: time.Millisecond, Concurrency: 3}, false},
		{"negative interval", Throttle{Interval: -time.Millisecond}, Throttle{}, true},
		{"negative concurrency", Throttle{Concurrency: -1}, Throttle{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.in.withDefaults(def, "generate")
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrClient) {
					t.Fatalf("err = %v, want ErrInvalidInput and ErrClient", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("withDefaults() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("withDefaults() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// resolveWith applies opts to a default config and resolves it.
func resolveWith(opts ...Option) (*resolvedConfig, error) {
	cfg := &config{baseURL: DefaultBaseURL, userAgent: "test/1.0", timeout: DefaultTimeout}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg.resolve()
}

func TestNewClientDefaultThrottles(t *testing.T) {
	r, err := resolveWith()
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if want := (Throttle{Interval: 200 * time.Millisecond, Concurrency: 1}); r.generateThrottle != want {
		t.Errorf("generate = %+v, want %+v", r.generateThrottle, want)
	}
	if want := (Throttle{Interval: 100 * time.Millisecond, Concurrency: 5}); r.getThrottle != want {
		t.Errorf("get = %+v, want %+v", r.getThrottle, want)
	}
	if r.autoTuning {
		t.Error("autoTuning = true, want false")
	}
}

func TestDefaultThrottleConstants(t *testing.T) {
	if DefaultGenerateInterval != 200*time.Millisecond || DefaultGenerateConcurrency != 1 ||
		DefaultGetInterval != 100*time.Millisecond || DefaultGetConcurrency != 5 {
		t.Error("default throttle constants differ from the documented values")
	}
}

func TestNewClientThrottleOptions(t *testing.T) {
	gen := Throttle{Interval: time.Second, Concurrency: 2}
	get := Throttle{Interval: 10 * time.Millisecond, Concurrency: 9}

	r, err := resolveWith(WithGenerateThrottle(gen), WithGetThrottle(get))
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if r.generateThrottle != gen || r.getThrottle != get || r.autoTuning {
		t.Errorf("got gen=%+v get=%+v auto=%v", r.generateThrottle, r.getThrottle, r.autoTuning)
	}

	r, err = resolveWith(WithAutoTuning(), WithGetThrottle(get))
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if !r.autoTuning || r.getThrottle != get {
		t.Errorf("got get=%+v auto=%v, want get=%+v auto=true", r.getThrottle, r.autoTuning, get)
	}

	r, err = resolveWith(WithGetThrottle(Throttle{Concurrency: 2}))
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if want := (Throttle{Interval: DefaultGetInterval, Concurrency: 2}); r.getThrottle != want {
		t.Errorf("partial get = %+v, want %+v", r.getThrottle, want)
	}
}

func TestNewClientRejectsNegativeThrottle(t *testing.T) {
	tests := []struct {
		name string
		opt  Option
	}{
		{"generate interval", WithGenerateThrottle(Throttle{Interval: -1})},
		{"generate concurrency", WithGenerateThrottle(Throttle{Concurrency: -1})},
		{"get interval", WithGetThrottle(Throttle{Interval: -1})},
		{"get concurrency", WithGetThrottle(Throttle{Concurrency: -1})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(WithUserAgent("test/1.0"), tt.opt)
			if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrClient) {
				t.Fatalf("NewClient() err = %v, want ErrInvalidInput and ErrClient", err)
			}
		})
	}
}

func TestNewClientRejectsAutoTuningWithGenerateThrottle(t *testing.T) {
	gen := WithGenerateThrottle(Throttle{Interval: time.Second, Concurrency: 1})
	tests := []struct {
		name string
		opts []Option
	}{
		{"auto-tuning first", []Option{WithAutoTuning(), gen}},
		{"throttle first", []Option{gen, WithAutoTuning()}},
		{"zero throttle still conflicts", []Option{WithAutoTuning(), WithGenerateThrottle(Throttle{})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(append([]Option{WithUserAgent("test/1.0")}, tt.opts...)...)
			if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrClient) {
				t.Fatalf("NewClient() err = %v, want ErrInvalidInput and ErrClient", err)
			}
		})
	}
}

func TestNewClientDoesNoIOWithAutoTuning(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	if _, err := NewClient(WithBaseURL(srv.URL), WithUserAgent("test/1.0"), WithAutoTuning()); err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("server hits = %d, want 0", got)
	}
}
