package mineskin

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var testCape = UUID{0x01, 0x23, 0xab, 0xcd, 0x45, 0x67, 0x89, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}

const testCapeDashed = "0123abcd-4567-89ef-0123-456789abcdef"

func fullOptions() *GenerateOptions {
	return &GenerateOptions{Name: "my skin", Variant: VariantSlim, Visibility: VisibilityUnlisted, Cape: testCape}
}

func TestSourceJSONBodies(t *testing.T) {
	user := UUID{0xde, 0xad, 0xbe, 0xef, 0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6}
	tests := []struct {
		name string
		src  Source
		opts *GenerateOptions
		want map[string]any
	}{
		{"url nil opts", FromURL("https://example.com/a.png"), nil, map[string]any{"url": "https://example.com/a.png"}},
		{"url zero opts", FromURL("https://example.com/a.png"), &GenerateOptions{Variant: VariantAuto}, map[string]any{"url": "https://example.com/a.png"}},
		{"user dashed", FromUser(user), nil, map[string]any{"user": user.String()}},
		{"all options", FromURL("https://example.com/a.png"), fullOptions(), map[string]any{
			"url": "https://example.com/a.png", "name": "my skin", "variant": "slim",
			"visibility": "unlisted", "cape": testCapeDashed,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := tt.src.requestBody(tt.opts)
			if err != nil {
				t.Fatalf("requestBody() error = %v", err)
			}
			if body.ContentType != "application/json" {
				t.Errorf("contentType = %q, want application/json", body.ContentType)
			}
			var got map[string]any
			if err := json.Unmarshal(body.Data, &got); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("body = %v, want %v", got, tt.want)
			}
		})
	}
}

func writeTempSkin(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "any-name.dat")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	for x := 0; x < 4; x++ {
		for y := 0; y < 3; y++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 40), G: uint8(y * 60), B: 7, A: 255})
		}
	}
	return img
}

func TestSourceMultipart(t *testing.T) {
	content := []byte("\x89PNG fake bytes \x00\x01\xff")
	img := testImage()
	var imgBuf bytes.Buffer
	if err := png.Encode(&imgBuf, img); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		src  Source
		want []byte
	}{
		{"reader", FromReader(bytes.NewReader(content)), content},
		{"file", FromFile(writeTempSkin(t, content)), content},
		{"image", FromImage(img), imgBuf.Bytes()},
	}
	for _, tt := range tests {
		t.Run(tt.name+" with options", func(t *testing.T) {
			body, err := tt.src.requestBody(fullOptions())
			if err != nil {
				t.Fatalf("requestBody() error = %v", err)
			}
			parts := parseMultipart(t, body)
			wantNames := []string{"name", "variant", "visibility", "cape", "file"}
			if len(parts) != len(wantNames) {
				t.Fatalf("got %d parts, want %d", len(parts), len(wantNames))
			}
			wantText := []string{"my skin", "slim", "unlisted", testCapeDashed}
			for i, p := range parts[:4] {
				if p.name != wantNames[i] || string(p.data) != wantText[i] || p.filename != "" {
					t.Errorf("part %d = %+v, want %s=%q", i, p, wantNames[i], wantText[i])
				}
			}
			f := parts[4]
			if f.name != "file" || f.filename != "skin.png" || f.contentType != "image/png" {
				t.Errorf("file part = name %q filename %q type %q", f.name, f.filename, f.contentType)
			}
			if !bytes.Equal(f.data, tt.want) {
				t.Errorf("file bytes differ")
			}
		})
		t.Run(tt.name+" zero options", func(t *testing.T) {
			src := tt.src
			if tt.name == "reader" {
				src = FromReader(bytes.NewReader(content))
			}
			body, err := src.requestBody(nil)
			if err != nil {
				t.Fatalf("requestBody() error = %v", err)
			}
			parts := parseMultipart(t, body)
			if len(parts) != 1 || parts[0].name != "file" {
				t.Errorf("parts = %+v, want only the file part", parts)
			}
		})
	}
}

type closeSpy struct {
	io.Reader
	closed bool
}

func (c *closeSpy) Close() error { c.closed = true; return nil }

func TestFromReaderNotClosedReadOnce(t *testing.T) {
	spy := &closeSpy{Reader: strings.NewReader("skin-data")}
	src := FromReader(spy)
	body, err := src.requestBody(nil)
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	if spy.closed {
		t.Error("reader was closed")
	}
	parts := parseMultipart(t, body)
	if len(parts) != 1 || string(parts[0].data) != "skin-data" {
		t.Errorf("parts = %+v", parts)
	}
	second, err := src.requestBody(nil)
	if err != nil {
		t.Fatalf("second requestBody() error = %v", err)
	}
	if p := parseMultipart(t, second); len(p) != 1 || len(p[0].data) != 0 {
		t.Errorf("second encode should see an exhausted reader, got %+v", p)
	}
}

func TestFromFileReadsOnEverySend(t *testing.T) {
	content := []byte("file-content")
	src := FromFile(writeTempSkin(t, content))
	for i := 0; i < 2; i++ {
		body, err := src.requestBody(nil)
		if err != nil {
			t.Fatalf("encode %d error = %v", i, err)
		}
		parts := parseMultipart(t, body)
		if len(parts) != 1 || !bytes.Equal(parts[0].data, content) {
			t.Errorf("encode %d parts = %+v", i, parts)
		}
	}
}

func TestFromFileMissing(t *testing.T) {
	_, err := FromFile(filepath.Join(t.TempDir(), "missing.png")).requestBody(nil)
	if !errors.Is(err, ErrClient) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want ErrClient and fs.ErrNotExist", err)
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Error("missing file must not match ErrInvalidInput")
	}
}

func TestFromImageEncodesPNG(t *testing.T) {
	img := testImage()
	body, err := FromImage(img).requestBody(nil)
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	parts := parseMultipart(t, body)
	got, err := png.Decode(bytes.NewReader(parts[0].data))
	if err != nil {
		t.Fatalf("png.Decode() error = %v", err)
	}
	if got.Bounds() != img.Bounds() {
		t.Fatalf("bounds = %v, want %v", got.Bounds(), img.Bounds())
	}
	for x := 0; x < 4; x++ {
		for y := 0; y < 3; y++ {
			if color.NRGBAModel.Convert(got.At(x, y)) != img.At(x, y) {
				t.Errorf("pixel (%d,%d) differs", x, y)
			}
		}
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestFromReaderError(t *testing.T) {
	cause := errors.New("disk on fire")
	_, err := FromReader(errReader{cause}).requestBody(nil)
	if !errors.Is(err, ErrClient) || !errors.Is(err, cause) {
		t.Errorf("error = %v, want ErrClient wrapping cause", err)
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Error("reader failure must not match ErrInvalidInput")
	}
}

func TestSourceInvalid(t *testing.T) {
	invalid := map[string]Source{
		"nil reader":    FromReader(nil),
		"nil image":     FromImage(nil),
		"empty path":    FromFile(""),
		"empty url":     FromURL(""),
		"not a url":     FromURL("not a url"),
		"relative path": FromURL("/relative/path"),
		"no host":       FromURL("https://"),
		"bad escape":    FromURL("%zz"),
	}
	for name, src := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := src.requestBody(nil); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
	if _, err := FromURL("https://example.com/a.png").requestBody(nil); err != nil {
		t.Errorf("valid URL error = %v", err)
	}
}
