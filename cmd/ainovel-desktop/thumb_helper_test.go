package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// bigPNG 造一张有噪声的 PNG，避免纯色被 PNG 压到极小而失去代表性。
func bigPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * y % 251), uint8(x % 253), uint8(y % 249), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
