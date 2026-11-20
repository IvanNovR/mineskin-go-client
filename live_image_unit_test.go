package mineskin

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

const (
	// MineSkin accepts uploads of 1 KB to 20 KB.
	liveMinPNGBytes = 1 << 10
	liveMaxPNGBytes = 20 << 10
)

func TestRandomSkinImage_OpaqueOnlyOnBaseLayer(t *testing.T) {
	t.Parallel()
	img := randomSkinImage()

	if got := img.Bounds(); got != image.Rect(0, 0, 64, 64) {
		t.Fatalf("bounds = %v, want 64x64", got)
	}
	inBox := func(x, y int) bool {
		for _, b := range baseLayerBoxes {
			if image.Pt(x, y).In(b) {
				return true
			}
		}
		return false
	}
	for y := range 64 {
		for x := range 64 {
			a := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A
			switch {
			case inBox(x, y) && a != 0xff:
				t.Fatalf("pixel (%d,%d) in a base-layer box has alpha %d, want 255", x, y, a)
			case !inBox(x, y) && a != 0:
				t.Fatalf("pixel (%d,%d) outside the base layer has alpha %d, want 0", x, y, a)
			}
		}
	}
}

func TestRandomSkinImage_PNGSizeIsWithinMineSkinWindow(t *testing.T) {
	t.Parallel()
	for i := range 20 {
		var buf bytes.Buffer
		if err := png.Encode(&buf, randomSkinImage()); err != nil {
			t.Fatal(err)
		}
		if n := buf.Len(); n < liveMinPNGBytes || n > liveMaxPNGBytes {
			t.Fatalf("run %d: PNG is %d bytes, want %d..%d", i, n, liveMinPNGBytes, liveMaxPNGBytes)
		}
	}
}

func TestRandomSkinImage_IsUniquePerCall(t *testing.T) {
	t.Parallel()
	a, b := randomSkinImage().(*image.NRGBA), randomSkinImage().(*image.NRGBA)
	if bytes.Equal(a.Pix, b.Pix) {
		t.Error("two images are identical, want unique per call")
	}
}
