package mineskin_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client"
)

func TestGetSkinDecodesFullSkin(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("MineSkin-Breadcrumb", "crumb1")
		writeJSON(200, skinFixture)(w, r)
	})
	got, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinUUID)
	if err != nil {
		t.Fatalf("GetSkin() error = %v", err)
	}
	s := got.Skin
	if s.UUID.String() != testSkinID || s.Name != "Steve" || s.Variant != mineskin.VariantSlim ||
		s.Visibility != mineskin.VisibilityUnlisted || s.Views != 7 || !s.Duplicate {
		t.Errorf("skin = %+v", s)
	}
	if s.Texture.Data.Value != "VAL" || s.Texture.Data.Signature != "SIG" || s.Texture.Hash.Skin != "sh" ||
		s.Texture.Hash.Cape != "ch" || s.Texture.URL.Skin != "su" || s.Texture.URL.Cape != "cu" {
		t.Errorf("texture = %+v", s.Texture)
	}
	if !s.Generator.Timestamp.Equal(time.UnixMilli(1700000000123)) || s.Generator.Duration != 1500*time.Millisecond {
		t.Errorf("generator = %+v", s.Generator)
	}
	if got.Meta.Breadcrumb != "crumb1" || got.Meta.Status != 200 {
		t.Errorf("meta = %+v", got.Meta)
	}
}

func TestGetSkinMissingGeneratorIsZero(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"absent": `{"success":true,"skin":{"uuid":"744c993e0e8a4f0b9b6a2f3c7a1b5d11"}}`,
		"null":   `{"success":true,"skin":{"uuid":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","generator":null}}`,
		"empty":  `{"success":true,"skin":{"uuid":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","generator":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t, writeJSON(200, body))
			got, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinUUID)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Skin.Generator.Timestamp.IsZero() || got.Skin.Generator.Duration != 0 {
				t.Errorf("generator = %+v", got.Skin.Generator)
			}
		})
	}
}

func TestGetSkinKeepsUnknownAndEmptyVariant(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(200, `{"success":true,"skin":{"variant":"","visibility":"weird"}}`))
	got, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinUUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Skin.Variant != mineskin.VariantAuto || got.Skin.Visibility != mineskin.Visibility("weird") {
		t.Errorf("skin = %+v", got.Skin)
	}
	srv2 := newTestServer(t, writeJSON(200, `{"success":true,"skin":{"variant":"future"}}`))
	got, err = newTestClient(t, srv2.URL).GetSkin(context.Background(), testSkinUUID)
	if err != nil || got.Skin.Variant != mineskin.Variant("future") {
		t.Errorf("variant = %q err = %v", got.Skin.Variant, err)
	}
}

func TestGetSkinRejectsZeroUUIDWithoutRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := newTestServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	got, err := newTestClient(t, srv.URL).GetSkin(context.Background(), mineskin.UUID{})
	if got != nil || !errors.Is(err, mineskin.ErrInvalidInput) || err.Error() != "mineskin: invalid input: skin id is the zero UUID" {
		t.Fatalf("got (%v, %v), want nil and ErrInvalidInput with the zero-UUID message", got, err)
	}
	if hits.Load() != 0 {
		t.Error("request was sent")
	}
}

func TestGetSkinSendsDashedForm(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]string{
		"undashed": "744c993e0e8a4f0b9b6a2f3c7a1b5d11",
		"upper":    "744C993E-0E8A-4F0B-9B6A-2F3C7A1B5D11",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var uri string
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				uri = r.RequestURI
				writeJSON(200, skinFixture)(w, r)
			})
			if _, err := newTestClient(t, srv.URL).GetSkin(context.Background(), fixtureUUID(id)); err != nil {
				t.Fatal(err)
			}
			if want := "/v2/skins/" + testSkinID; uri != want {
				t.Errorf("RequestURI = %q, want %q", uri, want)
			}
		})
	}
}

func TestGetSkinNotFoundIsAPIErrorNotParseError(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(404, `{"success":false,"errors":[{"code":"skin_not_found","message":"Skin not found"}]}`))
	got, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinUUID)
	if got != nil {
		t.Errorf("response = %+v, want nil", got)
	}
	var apiErr *mineskin.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %v, want *APIError", err, err)
	}
	if !errors.Is(err, mineskin.ErrNotFound) || apiErr.Meta.Status != 404 || apiErr.Message != "Skin not found" || apiErr.Code != "skin_not_found" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestGetSkinHTMLBadGateway(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(502, `<html>Bad Gateway</html>`))
	_, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinUUID)
	var ue *mineskin.UnexpectedResponseError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %T %v, want *UnexpectedResponseError", err, err)
	}
}

func TestGetSkinWrongTypedFieldsAreParseErrors(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"skin string":     `{"success":true,"skin":"x"}`,
		"views string":    `{"success":true,"skin":{"views":"7"}}`,
		"generator array": `{"success":true,"skin":{"generator":[1]}}`,
		"bad uuid":        `{"success":true,"skin":{"uuid":"not-a-uuid"}}`,
		"numeric uuid":    `{"success":true,"skin":{"uuid":5}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t, writeJSON(200, body))
			_, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinUUID)
			assertParseError(t, err)
		})
	}
}

func TestGeneratorInfoUnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in      string
		wantTS  int64
		wantDur time.Duration
		wantErr bool
	}{
		"null":             {in: `null`},
		"empty object":     {in: `{}`},
		"null fields":      {in: `{"timestamp":null,"duration":null}`},
		"millis":           {in: `{"timestamp":1700000000000,"duration":250}`, wantTS: 1700000000000, wantDur: 250 * time.Millisecond},
		"timestamp float":  {in: `{"timestamp":1.5}`, wantErr: true},
		"duration string":  {in: `{"duration":"5"}`, wantErr: true},
		"not an object":    {in: `5`, wantErr: true},
		"timestamp bool":   {in: `{"timestamp":true}`, wantErr: true},
		"timestamp string": {in: `{"timestamp":"1"}`, wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var g mineskin.GeneratorInfo
			err := g.UnmarshalJSON([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if tc.wantTS == 0 && !g.Timestamp.IsZero() || tc.wantTS != 0 && !g.Timestamp.Equal(time.UnixMilli(tc.wantTS)) {
				t.Errorf("Timestamp = %v", g.Timestamp)
			}
			if g.Duration != tc.wantDur {
				t.Errorf("Duration = %v", g.Duration)
			}
		})
	}
}

func TestGetSkinWrongTypedPayloadOnErrorResponseIsAPIError(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		status int
		body   string
		is     error
	}{
		"404 skin string": {404, `{"success":false,"skin":"gone"}`, mineskin.ErrNotFound},
		"429 json error":  {429, `{"success":false,"skin":[1],"errors":[{"code":"rate_limit","message":"slow down"}]}`, nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t, writeJSON(tc.status, tc.body))
			_, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinUUID)
			var apiErr *mineskin.APIError
			if !errors.As(err, &apiErr) || apiErr.Meta.Status != tc.status {
				t.Fatalf("err = %T %v, want *APIError %d", err, err, tc.status)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("err = %v, want %v", err, tc.is)
			}
		})
	}
}

func TestGeneratorInfoDurationBounds(t *testing.T) {
	t.Parallel()
	maxMillis := int64(math.MaxInt64 / int64(time.Millisecond))
	tests := map[string]struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		"max ok":       {in: fmt.Sprintf(`{"duration":%d}`, maxMillis), want: time.Duration(maxMillis) * time.Millisecond},
		"overflow":     {in: fmt.Sprintf(`{"duration":%d}`, maxMillis+1), wantErr: true},
		"int64 max":    {in: fmt.Sprintf(`{"duration":%d}`, int64(math.MaxInt64)), wantErr: true},
		"negative":     {in: `{"duration":-1}`, wantErr: true},
		"zero":         {in: `{"duration":0}`},
		"min negative": {in: fmt.Sprintf(`{"duration":%d}`, int64(math.MinInt64)), wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var g mineskin.GeneratorInfo
			err := g.UnmarshalJSON([]byte(tc.in))
			if (err != nil) != tc.wantErr || (!tc.wantErr && g.Duration != tc.want) {
				t.Errorf("Duration = %v, err = %v", g.Duration, err)
			}
		})
	}
}
