package mineskin

import (
	"context"
	"encoding/json"
)

const (
	mePath   = "/v2/me"
	jsonNull = "null"

	grantPerMinute   = "per_minute"
	grantPerHour     = "per_hour"
	grantConcurrency = "concurrency"
)

// Grants describes what the API key of the current user may do. Zero means
// the API did not send a usable (positive integer) value.
type Grants struct {
	// PerMinute is the request limit per minute.
	PerMinute int
	// PerHour is the request limit per hour.
	PerHour int
	// Concurrency is the number of concurrent requests allowed.
	Concurrency int
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
// grant that is not a positive integer is left zero.
func (g *Grants) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	*g = Grants{
		Raw:         raw,
		PerMinute:   positiveInt(raw[grantPerMinute]),
		PerHour:     positiveInt(raw[grantPerHour]),
		Concurrency: positiveInt(raw[grantConcurrency]),
	}
	return nil
}

// positiveInt parses a JSON integer, returning 0 for anything else (absent,
// null, string, fraction) and for values below 1.
func positiveInt(v json.RawMessage) int {
	if n := lenientInt(v); n > 0 {
		return n
	}
	return 0
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
