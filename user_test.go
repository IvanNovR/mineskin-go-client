package mineskin

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestGetUserWithoutSuccessField(t *testing.T) {
	t.Parallel()
	var uri string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		uri = r.RequestURI
		writeJSON(200, `{"user":"744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11","grants":{"per_minute":60,"per_hour":900,"concurrency":10,"foo":true}}`)(w, r)
	})
	got, err := newTestClient(t, srv.URL).GetUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if uri != "/v2/me" || got.Meta.Status != 200 || got.User.UUID.String() != testSkinID {
		t.Errorf("uri=%q got=%+v", uri, got)
	}
	g := got.User.Grants
	if g.PerMinute != 60 || g.PerHour != 900 || g.Concurrency != 10 || string(g.Raw["foo"]) != "true" {
		t.Errorf("grants = %+v", g)
	}
}

func TestGetUserFullEnvelope(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(200, `{"success":true,"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","grants":{},
"warnings":[{"code":"w","message":"warn"}],"messages":[{"code":"m","message":"msg"}]}`))
	got, err := newTestClient(t, srv.URL).GetUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Meta.Warnings) != 1 || got.Meta.Warnings[0].Code != "w" || len(got.Meta.Messages) != 1 || got.Meta.Messages[0].Message != "msg" {
		t.Errorf("meta = %+v", got.Meta)
	}
}

func TestGetUserPerMinuteInvalidIsZero(t *testing.T) {
	t.Parallel()
	for name, v := range map[string]string{"zero": "0", "negative": "-5", "string": `"60"`, "float": "1.5", "null": "null", "bool": "true"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t, writeJSON(200, `{"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","grants":{"per_minute":`+v+`}}`))
			got, err := newTestClient(t, srv.URL).GetUser(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.User.Grants.PerMinute != 0 {
				t.Errorf("PerMinute = %d, want 0", got.User.Grants.PerMinute)
			}
		})
	}
}

func TestGetUserConcurrency(t *testing.T) {
	t.Parallel()
	tests := map[string]string{"absent": `{}`, "null": `{"concurrency":null}`, "string": `{"concurrency":"3"}`, "float": `{"concurrency":2.5}`,
		"zero": `{"concurrency":0}`, "negative": `{"concurrency":-2}`}
	for name, grants := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t, writeJSON(200, `{"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","grants":`+grants+`}`))
			got, err := newTestClient(t, srv.URL).GetUser(context.Background())
			if err != nil || got.User.Grants.Concurrency != 0 {
				t.Errorf("err=%v grants=%+v, want concurrency 0", err, got.User.Grants)
			}
		})
	}
}

func TestGetUserMissingGrantsIsZero(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11"}`, `{"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","grants":null}`} {
		srv := newTestServer(t, writeJSON(200, body))
		got, err := newTestClient(t, srv.URL).GetUser(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		g := got.User.Grants
		if g.PerMinute != 0 || g.PerHour != 0 || g.Concurrency != 0 || g.Raw != nil {
			t.Errorf("grants = %+v, want zero", g)
		}
	}
}

func TestGetUserUnauthorizedWithoutUserIsAPIError(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(401, `{"success":false,"errors":[{"code":"invalid_api_key","message":"Invalid API key"}]}`))
	got, err := newTestClient(t, srv.URL).GetUser(context.Background())
	var apiErr *APIError
	if got != nil || !errors.As(err, &apiErr) || apiErr.Meta.Status != 401 || apiErr.Code != "invalid_api_key" {
		t.Fatalf("got=%v err=%T %v", got, err, err)
	}
}

func TestGetUser403WithoutSuccessHasEmptyCode(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(403, `{}`))
	_, err := newTestClient(t, srv.URL).GetUser(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "" || apiErr.Message != "" || apiErr.Meta.Status != 403 {
		t.Fatalf("err = %T %v", err, err)
	}
}

func TestGetUserWrongTypedFieldsAreParseErrors(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"grants string": `{"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","grants":"x"}`,
		"grants array":  `{"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","grants":[1]}`,
		"user number":   `{"user":5}`,
		"user not uuid": `{"user":"not-a-uuid"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t, writeJSON(200, body))
			_, err := newTestClient(t, srv.URL).GetUser(context.Background())
			assertParseError(t, err)
		})
	}
}

func TestGetUserAbsentOrNullUserIsZeroUUID(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"grants":{}}`, `{"user":null,"grants":{}}`} {
		srv := newTestServer(t, writeJSON(200, body))
		got, err := newTestClient(t, srv.URL).GetUser(context.Background())
		if err != nil || got.User.UUID != (UUID{}) {
			t.Errorf("body %s: uuid=%v err=%v", body, got, err)
		}
	}
}

func TestGetUserMalformedUUIDIsPlainParseError(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(200, `{"user":"not-a-uuid"}`))
	_, err := newTestClient(t, srv.URL).GetUser(context.Background())
	assertParseError(t, err)
	if want := "mineskin: failed to parse response: invalid UUID: unrecognised format (length 10)"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err, want)
	}
}

func TestGetUserPerHourInvalidIsZero(t *testing.T) {
	t.Parallel()
	for name, v := range map[string]string{"zero": "0", "negative": "-5", "string": `"60"`, "float": "1.5", "null": "null", "absent": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			grants := `{}`
			if v != "" {
				grants = `{"per_hour":` + v + `}`
			}
			srv := newTestServer(t, writeJSON(200, `{"user":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","grants":`+grants+`}`))
			got, err := newTestClient(t, srv.URL).GetUser(context.Background())
			if err != nil || got.User.Grants.PerHour != 0 {
				t.Errorf("err=%v PerHour=%d, want 0", err, got.User.Grants.PerHour)
			}
		})
	}
}
