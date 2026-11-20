package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"
)

const (
	skinsPathPrefix = "/v2/skins/"

	// maxDurationMillis is the largest millisecond count a time.Duration holds.
	maxDurationMillis = int64(math.MaxInt64 / int64(time.Millisecond))
)

// Variant is the model of a skin: classic (wide arms) or slim.
type Variant string

// Known skin variants. Other values received from the API are kept as is.
const (
	// VariantAuto lets the API choose the variant; it is the empty string.
	VariantAuto Variant = ""
	// VariantClassic is the classic (Steve) model.
	VariantClassic Variant = "classic"
	// VariantSlim is the slim (Alex) model.
	VariantSlim Variant = "slim"
)

// Visibility controls who can see a skin.
type Visibility string

// Known skin visibilities. Other values received from the API are kept as is.
const (
	// VisibilityPublic skins are listed publicly.
	VisibilityPublic Visibility = "public"
	// VisibilityUnlisted skins are reachable by id only.
	VisibilityUnlisted Visibility = "unlisted"
	// VisibilityPrivate skins are visible to their owner only.
	VisibilityPrivate Visibility = "private"
)

// ValueAndSignature is a signed Mojang texture property.
type ValueAndSignature struct {
	Value     string `json:"value"`
	Signature string `json:"signature"`
}

// SkinHashes holds the texture hashes of a skin and its cape.
type SkinHashes struct {
	Skin string `json:"skin"`
	Cape string `json:"cape"`
}

// SkinURLs holds the texture URLs of a skin and its cape.
type SkinURLs struct {
	Skin string `json:"skin"`
	Cape string `json:"cape"`
}

// TextureInfo describes the texture of a skin.
type TextureInfo struct {
	Data ValueAndSignature `json:"data"`
	Hash SkinHashes        `json:"hash"`
	URL  SkinURLs          `json:"url"`
}

// GeneratorInfo describes when and how long the skin generation took.
//
// The API sends both fields as JSON integers. The unit is not documented;
// this client assumes milliseconds for both: Timestamp is milliseconds since
// the Unix epoch and Duration is a number of milliseconds. Absent or null
// fields leave the zero value.
type GeneratorInfo struct {
	Timestamp time.Time
	Duration  time.Duration
}

// UnmarshalJSON decodes the generator object, interpreting integers as
// milliseconds. A null value leaves g unchanged; a non-integer timestamp or
// duration is an error, as is a negative duration or one too large for a
// time.Duration.
func (g *GeneratorInfo) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	var fields struct {
		Timestamp json.RawMessage `json:"timestamp"`
		Duration  json.RawMessage `json:"duration"`
	}
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	ts, tsOK, err := optionalInt64(fields.Timestamp)
	if err != nil {
		return err
	}
	dur, durOK, err := optionalInt64(fields.Duration)
	if err != nil {
		return err
	}
	var out GeneratorInfo
	if tsOK {
		out.Timestamp = time.UnixMilli(ts)
	}
	if durOK {
		if dur < 0 || dur > maxDurationMillis {
			return fmt.Errorf("duration %d ms is out of range", dur)
		}
		out.Duration = time.Duration(dur) * time.Millisecond
	}
	*g = out
	return nil
}

// optionalInt64 parses a JSON integer. An absent or null value reports
// ok=false without an error; any other non-integer value is an error.
func optionalInt64(v json.RawMessage) (n int64, ok bool, err error) {
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return 0, false, nil
	}
	if v[0] == '"' {
		return 0, false, fmt.Errorf("expected an integer, got string %s", v)
	}
	var num json.Number
	if err := json.Unmarshal(v, &num); err != nil {
		return 0, false, err
	}
	n, err = strconv.ParseInt(num.String(), 10, 64)
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// Skin describes a skin stored by MineSkin. Its UUID is the MineSkin skin id,
// not a player UUID.
type Skin struct {
	UUID       UUID          `json:"uuid"`
	Name       string        `json:"name"`
	Variant    Variant       `json:"variant"`
	Visibility Visibility    `json:"visibility"`
	Texture    TextureInfo   `json:"texture"`
	Generator  GeneratorInfo `json:"generator"`
	Views      int           `json:"views"`
	Duplicate  bool          `json:"duplicate"`
}

// SkinResponse is the response to a skin lookup.
type SkinResponse struct {
	// Meta holds the status, headers and messages of the response.
	Meta ResponseMeta
	// Skin is the requested skin.
	Skin Skin
}

// GetSkin fetches a skin by its MineSkin skin id. The id must be a UUID in
// dashed or undashed form (any letter case); it is a MineSkin skin id, not a
// player UUID. An invalid id, including an empty one, fails with
// ErrInvalidInput without sending a request. The request always uses the
// canonical dashed lower-case form.
func (c *Client) GetSkin(ctx context.Context, id string) (*SkinResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	parsed, err := ParseUUID(id)
	if err != nil {
		return nil, err
	}
	wire, meta, err := getResponse[skinWire](ctx, c, skinsPathPrefix+url.PathEscape(parsed.String()), nil)
	if err != nil {
		return nil, err
	}
	return &SkinResponse{Meta: meta, Skin: wire.Skin}, nil
}

// GetSkinByUUID fetches a skin by its MineSkin skin id, sent in dashed form.
// The UUID is a MineSkin skin id, not a player UUID.
func (c *Client) GetSkinByUUID(ctx context.Context, id UUID) (*SkinResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	return c.GetSkin(ctx, id.String())
}

// skinWire is the wire shape of a skin lookup. An absent or null skin leaves
// the zero value so that error responses without a skin still produce an
// *APIError.
type skinWire struct {
	Skin Skin `json:"skin"`
}
