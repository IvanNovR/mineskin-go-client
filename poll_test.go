package mineskin

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestConstantInterval(t *testing.T) {
	iv := ConstantInterval(300 * time.Millisecond)
	for _, attempt := range []int{0, 1, 2, 100, 1 << 30} {
		if got := iv.Wait(attempt); got != 300*time.Millisecond {
			t.Errorf("attempt %d = %v, want 300ms", attempt, got)
		}
	}
}

func TestConstantIntervalRejectedByNewClient(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		c, err := NewClient(WithJobPolling(PollOptions{Interval: ConstantInterval(d)}))
		if c != nil || !errors.Is(err, ErrInvalidInput) ||
			err.Error() != "mineskin: invalid input: constant interval must be greater than zero" {
			t.Errorf("ConstantInterval(%v): got (%v, %v)", d, c, err)
		}
	}
	c, err := NewClient(WithJobPolling(PollOptions{Interval: ConstantInterval(time.Nanosecond)}))
	if err != nil {
		t.Fatalf("smallest positive interval rejected: %v", err)
	}
	_ = c.Close()
}

func TestPollIntervalFunc(t *testing.T) {
	var iv PollInterval = PollIntervalFunc(func(attempt int) time.Duration { return time.Duration(attempt) * time.Second })
	if got := iv.Wait(3); got != 3*time.Second {
		t.Errorf("Wait(3) = %v, want 3s", got)
	}
}

func TestBackoff(t *testing.T) {
	var iv PollInterval = Backoff{Initial: 200 * time.Millisecond, Max: 2 * time.Second, Multiplier: 2.0, FreeAttempts: 3}
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
		if got := iv.Wait(tt.attempt); got != tt.want {
			t.Errorf("attempt %d = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestBackoffPointerIsAPollInterval(t *testing.T) {
	var iv PollInterval = &Backoff{Initial: time.Second, Max: time.Second, Multiplier: 1}
	if got := iv.Wait(9); got != time.Second {
		t.Errorf("Wait(9) = %v, want 1s", got)
	}
}

func TestBackoffValidation(t *testing.T) {
	const ms = time.Millisecond
	tests := []struct {
		name    string
		b       Backoff
		wantErr string
	}{
		{"zero value", Backoff{}, "initial interval must be greater than zero"},
		{"zero initial", Backoff{Initial: 0, Max: time.Second, Multiplier: 2}, "initial interval must be greater than zero"},
		{"negative initial", Backoff{Initial: -ms, Max: time.Second, Multiplier: 2}, "initial interval must be greater than zero"},
		{"max below initial", Backoff{Initial: 2 * ms, Max: ms, Multiplier: 2}, "max interval must not be below the initial interval"},
		{"multiplier below one", Backoff{Initial: ms, Max: time.Second, Multiplier: 0.5}, "multiplier must be a finite number of at least 1"},
		{"NaN multiplier", Backoff{Initial: ms, Max: time.Second, Multiplier: math.NaN()}, "multiplier must be a finite number of at least 1"},
		{"+Inf multiplier", Backoff{Initial: ms, Max: time.Second, Multiplier: math.Inf(1)}, "multiplier must be a finite number of at least 1"},
		{"-Inf multiplier", Backoff{Initial: ms, Max: time.Second, Multiplier: math.Inf(-1)}, "multiplier must be a finite number of at least 1"},
		{"negative free attempts", Backoff{Initial: ms, Max: time.Second, Multiplier: 2, FreeAttempts: -1}, "free attempts must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(WithJobPolling(PollOptions{Interval: tt.b}))
			want := "mineskin: invalid input: " + tt.wantErr
			if c != nil || !errors.Is(err, ErrInvalidInput) || err.Error() != want {
				t.Fatalf("got (%v, %v), want nil client and %q", c, err, want)
			}
		})
	}
	c, err := NewClient(WithJobPolling(PollOptions{Interval: Backoff{Initial: time.Second, Max: time.Second, Multiplier: 1}}))
	if err != nil {
		t.Errorf("boundary values rejected: %v", err)
	} else {
		_ = c.Close()
	}
}

func TestWithJobPollingDefaults(t *testing.T) {
	custom := PollIntervalFunc(func(int) time.Duration { return 5 * time.Millisecond })
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
			if got.Interval == nil || got.Interval.Wait(1) != tt.wantInterval {
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
	if p.Interval == nil || p.Interval.Wait(7) != time.Second {
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
