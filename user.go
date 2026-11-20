package mineskin

import (
	"context"
	"encoding/json"
	"math"
)

const (
	mePath   = "/v2/me"
	jsonNull = "null"

	grantPerMinute   = "per_minute"
	grantConcurrency = "concurrency"
)

// Grants describes what the API key of the current user may do.
type Grants struct {
	// PerMinute is the request limit per minute, or nil when the API did not
	// send a positive integer.
	PerMinute *int
	// Concurrency is the number of concurrent requests allowed, or nil when
	// the API did not send an integer.
	Concurrency *int
	// Raw holds every grant as sent by the API, or nil when there were none.
	Raw map[string]json.RawMessage
}

// User describes the current user.
type User struct {
	// UUID is the value of the "user" field of the response; it is the zero
	// UUID when the field is absent or null.
	UUID UUID
	// Grants are the grants of the user's API key.
	Grants Grants
}

// UserResponse is the response to GetUser.
type UserResponse struct {
	// Meta holds the status, headers and messages of the response.
	Meta ResponseMeta
	// User is the current user.
	User User
}

// userWire is the wire shape of a /v2/me response.
type userWire struct {
	User   UUID   `json:"user"`
	Grants Grants `json:"grants"`
}

// UnmarshalJSON decodes the grants object. A null value leaves g unchanged; a
// value that is not an object is an error. Inside the object it is lenient: a
// per_minute that is not a positive integer, or a concurrency that is not an
// integer, is left nil.
func (g *Grants) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	*g = Grants{Raw: raw, PerMinute: positiveIntPtr(raw[grantPerMinute]), Concurrency: intPtr(raw[grantConcurrency])}
	return nil
}

// intPtr parses a JSON integer, returning nil for anything else (absent,
// null, string, fraction).
func intPtr(v json.RawMessage) *int {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok || n < math.MinInt || n > math.MaxInt {
		return nil
	}
	i := int(n)
	return &i
}

// positiveIntPtr is like intPtr but also returns nil for values below 1.
func positiveIntPtr(v json.RawMessage) *int {
	n := intPtr(v)
	if n == nil || *n <= 0 {
		return nil
	}
	return n
}

// GetUser fetches the user and grants of the configured API key. Missing user
// or grants fields are tolerated so that error responses yield an *APIError.
func (c *Client) GetUser(ctx context.Context) (*UserResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	wire, meta, err := getResponse[userWire](ctx, c, mePath, nil)
	if err != nil {
		return nil, err
	}
	return &UserResponse{Meta: meta, User: User{UUID: wire.User, Grants: wire.Grants}}, nil
}
