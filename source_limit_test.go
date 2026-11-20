package mineskin

import (
	"bytes"
	"errors"
	"image"
	"math/rand"
	"strings"
	"testing"
)

// endlessReader never returns EOF.
type endlessReader struct{ read int64 }

func (r *endlessReader) Read(p []byte) (int, error) {
	r.read += int64(len(p))
	return len(p), nil
}

func assertUploadTooLarge(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "1 MiB") {
		t.Errorf("error %q does not mention the 1 MiB limit", err)
	}
}

func TestFromReaderUploadLimit(t *testing.T) {
	t.Parallel()
	t.Run("exactly the limit passes", func(t *testing.T) {
		t.Parallel()
		body, err := FromReader(bytes.NewReader(make([]byte, maxUploadBytes))).requestBody(GenerateOptions{})
		if err != nil {
			t.Fatalf("requestBody() error = %v", err)
		}
		if got := parseMultipart(t, body)[0].data; len(got) != maxUploadBytes {
			t.Errorf("uploaded %d bytes, want %d", len(got), maxUploadBytes)
		}
	})
	t.Run("one byte over fails", func(t *testing.T) {
		t.Parallel()
		_, err := FromReader(bytes.NewReader(make([]byte, maxUploadBytes+1))).requestBody(GenerateOptions{})
		assertUploadTooLarge(t, err)
	})
	t.Run("endless reader fails after bounded read", func(t *testing.T) {
		t.Parallel()
		r := &endlessReader{}
		_, err := FromReader(r).requestBody(GenerateOptions{})
		assertUploadTooLarge(t, err)
		if r.read > 2*maxUploadBytes {
			t.Errorf("read %d bytes from the reader, want a bounded read", r.read)
		}
	})
}

func TestFromFileUploadLimit(t *testing.T) {
	t.Parallel()
	t.Run("exactly the limit passes", func(t *testing.T) {
		t.Parallel()
		body, err := FromFile(writeTempSkin(t, make([]byte, maxUploadBytes))).requestBody(GenerateOptions{})
		if err != nil {
			t.Fatalf("requestBody() error = %v", err)
		}
		if got := parseMultipart(t, body)[0].data; len(got) != maxUploadBytes {
			t.Errorf("uploaded %d bytes, want %d", len(got), maxUploadBytes)
		}
	})
	t.Run("one byte over fails", func(t *testing.T) {
		t.Parallel()
		_, err := FromFile(writeTempSkin(t, make([]byte, maxUploadBytes+1))).requestBody(GenerateOptions{})
		assertUploadTooLarge(t, err)
	})
}

func TestFromImageUploadLimit(t *testing.T) {
	t.Parallel()
	img := image.NewNRGBA(image.Rect(0, 0, 600, 600))
	rand.New(rand.NewSource(1)).Read(img.Pix) //nolint:gosec // test noise
	_, err := FromImage(img).requestBody(GenerateOptions{})
	assertUploadTooLarge(t, err)
}
