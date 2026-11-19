package mineskin

import "time"

const (
	// DefaultGenerateInterval is the default minimum time between two starts
	// in the generate group (Queue and Generate).
	DefaultGenerateInterval = 200 * time.Millisecond
	// DefaultGenerateConcurrency is the default number of generate-group
	// requests allowed in flight.
	DefaultGenerateConcurrency = 1
	// DefaultGetInterval is the default minimum time between two starts in
	// the get group (GetSkin, GetUser, GetJob, ListJobs and job polling).
	DefaultGetInterval = 100 * time.Millisecond
	// DefaultGetConcurrency is the default number of get-group requests
	// allowed in flight.
	DefaultGetConcurrency = 5
)

// Throttle limits one request group. Queued requests start in FIFO order and
// a request starts only when a slot is free, Interval has passed since the
// previous start and any delay requested by the server has elapsed.
//
// Zero fields select the defaults of the group (see the Default* constants);
// negative fields are invalid and make NewClient return an error matching
// ErrInvalidInput. Time spent queued is bounded only by the caller's context:
// WithTimeout covers the HTTP exchange alone. Throttling cannot be turned
// off.
type Throttle struct {
	// Interval is the minimum time between two request starts.
	Interval time.Duration
	// Concurrency is the maximum number of requests in flight.
	Concurrency int
}

// withDefaults returns t with zero fields taken from def, or an error
// matching ErrInvalidInput if a field is negative. group names the group in
// the error message.
func (t Throttle) withDefaults(def Throttle, group string) (Throttle, error) {
	if t.Interval < 0 {
		return Throttle{}, newInvalidInputError(group + " throttle interval must not be negative")
	}
	if t.Concurrency < 0 {
		return Throttle{}, newInvalidInputError(group + " throttle concurrency must not be negative")
	}
	if t.Interval == 0 {
		t.Interval = def.Interval
	}
	if t.Concurrency == 0 {
		t.Concurrency = def.Concurrency
	}
	return t, nil
}
