package mineskin

import (
	"context"
	"encoding/json"
	"strconv"
)

const (
	mePath             = "/v2/me"
	fieldUser          = "user"
	fieldGrants        = "grants"
	fieldPerMinute     = "per_minute"
	fieldConcurrency   = "concurrency"
	jsonNull           = "null"
	jsonStringQuoteTag = '"'
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

// UserInfo describes the current user.
type UserInfo struct {
	// UUID is the value of the "user" field of the response; it is the zero
	// UUID when the field is absent or null.
	UUID UUID
	// Grants are the grants of the user's API key.
	Grants Grants
}

// UserResponse is the response to GetUser.
type UserResponse struct {
	ResponseMeta
	// User is the current user.
	User UserInfo
}

// GetUser fetches the user and grants of the configured API key. Missing user
// or grants fields are tolerated so that error responses yield an *APIError.
func (c *Client) GetUser(ctx context.Context) (*UserResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	var user UserInfo
	meta, err := c.get(ctx, mePath, func(obj map[string]json.RawMessage, _ []byte) error {
		return decodeUser(obj, &user)
	})
	if err != nil {
		return nil, err
	}
	return &UserResponse{ResponseMeta: meta, User: user}, nil
}

// decodeUser fills dst from the top-level fields of a /v2/me response.
func decodeUser(obj map[string]json.RawMessage, dst *UserInfo) error {
	if v := obj[fieldUser]; len(v) > 0 {
		if err := json.Unmarshal(v, &dst.UUID); err != nil {
			return err
		}
	}
	return decodeGrants(obj[fieldGrants], &dst.Grants)
}

// decodeGrants decodes the grants object. Absent or null grants leave dst
// zero; a value that is not an object is an error.
func decodeGrants(v json.RawMessage, dst *Grants) error {
	if len(v) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(v, &raw); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	dst.Raw = raw
	dst.PerMinute = positiveInt(raw[fieldPerMinute])
	dst.Concurrency = integer(raw[fieldConcurrency])
	return nil
}

// integer parses a JSON integer, returning nil for anything else (absent,
// null, string, fraction).
func integer(v json.RawMessage) *int {
	if len(v) == 0 || v[0] == jsonStringQuoteTag || string(v) == jsonNull {
		return nil
	}
	var num json.Number
	if json.Unmarshal(v, &num) != nil {
		return nil
	}
	n, err := strconv.Atoi(num.String())
	if err != nil {
		return nil
	}
	return &n
}

// positiveInt is like integer but also returns nil for values below 1.
func positiveInt(v json.RawMessage) *int {
	n := integer(v)
	if n == nil || *n <= 0 {
		return nil
	}
	return n
}
