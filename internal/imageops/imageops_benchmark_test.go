package imageops

import (
	"image"
	"image/color"
	"testing"
)

func makeBenchNRGBA(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if (x+y)%11 == 0 {
				a = 200
			}
			if (x+y)%17 == 0 {
				a = 0
			}
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8((x * y) % 256), A: a})
		}
	}
	return img
}

func makeBenchRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if (x+y)%13 == 0 {
				a = 180
			}
			img.SetRGBA(x, y, color.RGBA{R: uint8((x * 3) % 256), G: uint8((y * 5) % 256), B: uint8((x + y) % 256), A: a})
		}
	}
	return img
}

func BenchmarkToNRGBAFromNRGBA(b *testing.B) {
	src := makeBenchNRGBA(1920, 1080)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = toNRGBA(src)
	}
}

func BenchmarkToNRGBAFromRGBA(b *testing.B) {
	src := makeBenchRGBA(1920, 1080)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = toNRGBA(src)
	}
}

func BenchmarkResizeToMaxWidth(b *testing.B) {
	src := makeBenchNRGBA(1920, 1080)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ResizeToMaxWidth(src, 1080)
		if err != nil {
			b.Fatalf("ResizeToMaxWidth error: %v", err)
		}
	}
}

func BenchmarkAutocropNonTransparent(b *testing.B) {
	src := makeBenchNRGBA(1280, 720)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = AutocropNonTransparent(src, true)
	}
}
