package mineskin

import (
	"crypto/rand"
	"fmt"
	"image"
	"image/color"
)

const liveSkinSize = 64

// baseLayerBoxes are the base-layer areas of a 64x64 skin: head, body and right
// limbs, left leg, left arm. Everything else (the overlay layer and unused
// areas) stays fully transparent.
var baseLayerBoxes = [...]image.Rectangle{
	image.Rect(0, 0, 32, 16),
	image.Rect(0, 16, 56, 32),
	image.Rect(16, 48, 32, 64),
	image.Rect(32, 48, 48, 64),
}

// randomSkinImage returns a 64x64 image that is fully transparent except for
// random opaque pixels on the base-layer boxes. It is unique per call and, as
// PNG, about 7 KB (MineSkin accepts 1 KB to 20 KB). Send it with VariantClassic.
func randomSkinImage() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, liveSkinSize, liveSkinSize))
	for _, box := range baseLayerBoxes {
		for y := box.Min.Y; y < box.Max.Y; y++ {
			for x := box.Min.X; x < box.Max.X; x++ {
				var rgb [3]byte
				if _, err := rand.Read(rgb[:]); err != nil {
					panic(fmt.Sprintf("rand.Read: %v", err))
				}
				img.SetNRGBA(x, y, color.NRGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 0xff})
			}
		}
	}
	return img
}
