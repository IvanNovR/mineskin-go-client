package mineskin

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestThrottleOrDefault(t *testing.T) {
	def := Throttle{Interval: 200 * time.Millisecond, Concurrency: 1}
	tests := []struct {
		name string
		in   Throttle
		want Throttle
	}{
		{"zero value uses defaults", Throttle{}, def},
		{"interval only keeps default concurrency", Throttle{Interval: time.Second}, Throttle{Interval: time.Second, Concurrency: 1}},
		{"concurrency only keeps default interval", Throttle{Concurrency: 7}, Throttle{Interval: 200 * time.Millisecond, Concurrency: 7}},
		{"both set are kept", Throttle{Interval: time.Millisecond, Concurrency: 3}, Throttle{Interval: time.Millisecond, Concurrency: 3}},
		{"negative interval is kept for validation", Throttle{Interval: -time.Millisecond}, Throttle{Interval: -time.Millisecond, Concurrency: 1}},
		{"negative concurrency is kept for validation", Throttle{Concurrency: -1}, Throttle{Interval: 200 * time.Millisecond, Concurrency: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.orDefault(def); got != tt.want {
				t.Errorf("orDefault() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestThrottleValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      Throttle
		wantErr string
	}{
		{"valid", Throttle{Interval: time.Second, Concurrency: 1}, ""},
		{"negative interval", Throttle{Interval: -time.Millisecond, Concurrency: 1}, "mineskin: invalid input: generate throttle interval must not be negative"},
		{"negative concurrency", Throttle{Interval: time.Second, Concurrency: -1}, "mineskin: invalid input: generate throttle concurrency must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.validate("generate")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidInput) || err.Error() != tt.wantErr {
				t.Fatalf("validate() = %v, want ErrInvalidInput %q", err, tt.wantErr)
			}
		})
	}
}

// configWith applies opts to a default config and validates it, the way
// NewClient does.
func configWith(opts ...Option) (*config, error) {
	cfg := defaultConfig()
	cfg.userAgent = "test/1.0"
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg, cfg.validate()
}

func TestNewClientDefaultThrottles(t *testing.T) {
	cfg, err := configWith()
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if want := (generatePacing{throttle: Throttle{Interval: 200 * time.Millisecond, Concurrency: 1}}); cfg.generate != want {
		t.Errorf("generate = %+v, want %+v", cfg.generate, want)
	}
	if want := (Throttle{Interval: 100 * time.Millisecond, Concurrency: 5}); cfg.getThrottle != want {
		t.Errorf("get = %+v, want %+v", cfg.getThrottle, want)
	}
}

func TestDefaultConfigValues(t *testing.T) {
	cfg := defaultConfig()
	if cfg.baseURL != DefaultBaseURL || cfg.userAgent != DefaultUserAgent ||
		cfg.timeout != 10*time.Second || cfg.rateLimitRetries != 2 ||
		cfg.logger == nil || cfg.clock == nil || !cfg.apiKey.IsEmpty() || cfg.transport != nil {
		t.Errorf("defaultConfig() = %+v", cfg)
	}
	if cfg.polling.InitialDelay != 2*time.Second || cfg.polling.MaxAttempts != 10 ||
		cfg.polling.UseETA || cfg.polling.Interval == nil {
		t.Errorf("default polling = %+v", cfg.polling)
	}
}

func TestNewClientThrottleOptions(t *testing.T) {
	gen := Throttle{Interval: time.Second, Concurrency: 2}
	get := Throttle{Interval: 10 * time.Millisecond, Concurrency: 9}

	cfg, err := configWith(WithGenerateThrottle(gen), WithGetThrottle(get))
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if cfg.generate != (generatePacing{throttle: gen}) || cfg.getThrottle != get {
		t.Errorf("got gen=%+v get=%+v", cfg.generate, cfg.getThrottle)
	}

	cfg, err = configWith(WithAutoTuning(), WithGetThrottle(get))
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if !cfg.generate.auto || cfg.getThrottle != get {
		t.Errorf("got get=%+v auto=%v, want get=%+v auto=true", cfg.getThrottle, cfg.generate.auto, get)
	}

	cfg, err = configWith(WithGetThrottle(Throttle{Concurrency: 2}))
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if want := (Throttle{Interval: 100 * time.Millisecond, Concurrency: 2}); cfg.getThrottle != want {
		t.Errorf("partial get = %+v, want %+v", cfg.getThrottle, want)
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
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("NewClient() err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestGeneratePacingLastOptionWins(t *testing.T) {
	gen := Throttle{Interval: time.Second, Concurrency: 3}
	tests := []struct {
		name string
		opts []Option
		want generatePacing
	}{
		{"auto-tuning then throttle", []Option{WithAutoTuning(), WithGenerateThrottle(gen)}, generatePacing{throttle: gen}},
		{"throttle then auto-tuning", []Option{WithGenerateThrottle(gen), WithAutoTuning()}, generatePacing{auto: true}},
		{"auto-tuning then zero throttle", []Option{WithAutoTuning(), WithGenerateThrottle(Throttle{})},
			generatePacing{throttle: Throttle{Interval: 200 * time.Millisecond, Concurrency: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := configWith(tt.opts...)
			if err != nil {
				t.Fatalf("validate() error = %v, want no conflict", err)
			}
			if cfg.generate != tt.want {
				t.Errorf("generate = %+v, want %+v", cfg.generate, tt.want)
			}
			c, err := NewClient(append([]Option{WithUserAgent("test/1.0")}, tt.opts...)...)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if hasTuner := c.genGroup.Tuner != nil; hasTuner != tt.want.auto {
				t.Errorf("tuner present = %v, want %v", hasTuner, tt.want.auto)
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
