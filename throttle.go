package mineskin

import "time"

var (
	// defaultGenerateThrottle paces Queue and Generate: one start per 200 ms,
	// one request in flight.
	defaultGenerateThrottle = Throttle{Interval: defaultGenerateInterval, Concurrency: defaultGenerateConcurrency}
	// defaultGetThrottle paces GetSkin, GetUser, GetJob, ListJobs and job
	// polling: one start per 100 ms, five requests in flight.
	defaultGetThrottle = Throttle{Interval: defaultGetInterval, Concurrency: defaultGetConcurrency}
)

const (
	defaultGenerateInterval    = 200 * time.Millisecond
	defaultGenerateConcurrency = 1
	defaultGetInterval         = 100 * time.Millisecond
	defaultGetConcurrency      = 5
)

// Throttle limits one request group. Queued requests start in FIFO order and
// a request starts only when a slot is free, Interval has passed since the
// previous start and any delay requested by the server has elapsed.
//
// Zero fields select the defaults of the group (WithGenerateThrottle and
// WithGetThrottle document them);
// negative fields are invalid and make NewClient return an error matching
// ErrInvalidInput. Time spent queued is bounded only by the caller's context:
// WithTimeout covers each HTTP attempt alone. Throttling cannot be turned
// off.
type Throttle struct {
	// Interval is the minimum time between two request starts.
	Interval time.Duration
	// Concurrency is the maximum number of requests in flight.
	Concurrency int
}

// orDefault returns t with zero fields taken from def. Negative fields are
// kept for validate to reject.
func (t Throttle) orDefault(def Throttle) Throttle {
	if t.Interval == 0 {
		t.Interval = def.Interval
	}
	if t.Concurrency == 0 {
		t.Concurrency = def.Concurrency
	}
	return t
}

// validate returns an error matching ErrInvalidInput if a field is negative.
// group names the group in the error message.
func (t Throttle) validate(group string) error {
	if t.Interval < 0 {
		return invalidInput("%s throttle interval must not be negative", group)
	}
	if t.Concurrency < 0 {
		return invalidInput("%s throttle concurrency must not be negative", group)
	}
	return nil
}
