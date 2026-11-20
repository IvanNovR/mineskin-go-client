package mineskin

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestConstantInterval(t *testing.T) {
	iv, err := ConstantInterval(300 * time.Millisecond)
	if err != nil {
		t.Fatalf("ConstantInterval() error = %v", err)
	}
	for _, attempt := range []int{0, 1, 2, 100, 1 << 30} {
		if got := iv(attempt); got != 300*time.Millisecond {
			t.Errorf("attempt %d = %v, want 300ms", attempt, got)
		}
	}
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := ConstantInterval(d); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ConstantInterval(%v) err = %v, want ErrInvalidInput", d, err)
		}
	}
}

func TestExponentialInterval(t *testing.T) {
	iv, err := ExponentialInterval(200*time.Millisecond, 2*time.Second, 2.0, 3)
	if err != nil {
		t.Fatalf("ExponentialInterval() error = %v", err)
	}
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 200 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 200 * time.Millisecond},
		{4, 400 * time.Millisecond},
		{5, 800 * time.Millisecond},
		{6, 1600 * time.Millisecond},
		{7, 2 * time.Second},
		{50, 2 * time.Second},
		{1 << 30, 2 * time.Second},
	}
	for _, tt := range tests {
		if got := iv(tt.attempt); got != tt.want {
			t.Errorf("attempt %d = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestExponentialIntervalValidation(t *testing.T) {
	const ms = time.Millisecond
	tests := []struct {
		name         string
		initial, max time.Duration
		mult         float64
		free         int
	}{
		{"zero initial", 0, time.Second, 2, 0},
		{"negative initial", -ms, time.Second, 2, 0},
		{"max below initial", 2 * ms, ms, 2, 0},
		{"multiplier below one", ms, time.Second, 0.5, 0},
		{"NaN multiplier", ms, time.Second, math.NaN(), 0},
		{"+Inf multiplier", ms, time.Second, math.Inf(1), 0},
		{"-Inf multiplier", ms, time.Second, math.Inf(-1), 0},
		{"negative free attempts", ms, time.Second, 2, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iv, err := ExponentialInterval(tt.initial, tt.max, tt.mult, tt.free)
			if !errors.Is(err, ErrInvalidInput) || iv != nil {
				t.Fatalf("got (%v, %v), want nil func and ErrInvalidInput", iv != nil, err)
			}
		})
	}
	if _, err := ExponentialInterval(time.Second, time.Second, 1, 0); err != nil {
		t.Errorf("boundary values rejected: %v", err)
	}
}

func TestWithJobPollingDefaults(t *testing.T) {
	custom := func(int) time.Duration { return 5 * time.Millisecond }
	tests := []struct {
		name         string
		in           PollOptions
		wantDelay    time.Duration
		wantInterval time.Duration
		wantMax      int
		wantETA      bool
	}{
		{"zero value", PollOptions{}, 2 * time.Second, time.Second, 10, false},
		{"partly set", PollOptions{MaxAttempts: 3, UseETA: true}, 2 * time.Second, time.Second, 3, true},
		{"custom interval and delay", PollOptions{InitialDelay: time.Millisecond, Interval: custom}, time.Millisecond, 5 * time.Millisecond, 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.withDefaults()
			if got.InitialDelay != tt.wantDelay || got.MaxAttempts != tt.wantMax || got.UseETA != tt.wantETA {
				t.Errorf("got %+v", got)
			}
			if got.Interval == nil || got.Interval(1) != tt.wantInterval {
				t.Errorf("interval wrong, want %v", tt.wantInterval)
			}
		})
	}
	if defaultPollInitialDelay != 2*time.Second || defaultPollInterval != time.Second || defaultPollMaxAttempts != 10 {
		t.Error("poll defaults changed")
	}
}

func TestWithJobPollingValidation(t *testing.T) {
	tests := []struct {
		name string
		in   PollOptions
	}{
		{"negative initial delay", PollOptions{InitialDelay: -time.Second}},
		{"negative max attempts", PollOptions{MaxAttempts: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(WithJobPolling(tt.in))
			if c != nil || !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got (%v, %v), want nil and ErrInvalidInput", c, err)
			}
		})
	}
}

func TestPollOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      PollOptions
		wantErr string
	}{
		{"valid", PollOptions{InitialDelay: time.Second, MaxAttempts: 1}, ""},
		{"negative initial delay", PollOptions{InitialDelay: -time.Second, MaxAttempts: 1}, "mineskin: invalid input: poll initial delay must not be negative"},
		{"negative max attempts", PollOptions{InitialDelay: time.Second, MaxAttempts: -1}, "mineskin: invalid input: poll max attempts must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidInput) || err.Error() != tt.wantErr {
				t.Fatalf("validate() = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewClientDefaultPolling(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	p := c.polling
	if p.InitialDelay != 2*time.Second || p.MaxAttempts != 10 || p.UseETA {
		t.Errorf("polling = %+v", p)
	}
	if p.Interval == nil || p.Interval(7) != time.Second {
		t.Error("default interval should be constant 1s")
	}
}

func TestNewClientCustomPolling(t *testing.T) {
	c, err := NewClient(WithJobPolling(PollOptions{MaxAttempts: 4, UseETA: true}))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.polling.MaxAttempts != 4 || !c.polling.UseETA || c.polling.InitialDelay != 2*time.Second {
		t.Errorf("polling = %+v", c.polling)
	}
}
