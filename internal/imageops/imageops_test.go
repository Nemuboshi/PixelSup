package imageops

import (
	"image"
	"image/color"
	"testing"
)

func TestAutocropNonTransparent_CropsOpaqueContent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for x := 2; x < 6; x++ {
		for y := 3; y < 8; y++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}

	cropped := AutocropNonTransparent(img, false)
	if got := cropped.Bounds().Size(); got.X != 4 || got.Y != 5 {
		t.Fatalf("cropped size mismatch: got=%dx%d want=4x5", got.X, got.Y)
	}
}

func TestAutocropNonTransparent_FallbackSolidBackground(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 12, 10))
	fillRect(img, 0, 0, 12, 10, color.NRGBA{A: 255})
	fillRect(img, 3, 2, 9, 8, color.NRGBA{R: 255, G: 255, B: 255, A: 255})

	cropped := AutocropNonTransparent(img, true)
	if got := cropped.Bounds().Size(); got.X != 6 || got.Y != 6 {
		t.Fatalf("cropped size mismatch: got=%dx%d want=6x6", got.X, got.Y)
	}
}

func TestAutocropNonTransparent_FallbackSkipsNonUniformCorners(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 12, 10))
	fillRect(img, 0, 0, 12, 10, color.NRGBA{A: 255})
	img.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 1, B: 1, A: 255})
	fillRect(img, 3, 2, 9, 8, color.NRGBA{R: 255, G: 255, B: 255, A: 255})

	cropped := AutocropNonTransparent(img, true)
	if got := cropped.Bounds().Size(); got.X != 12 || got.Y != 10 {
		t.Fatalf("cropped size mismatch: got=%dx%d want=12x10", got.X, got.Y)
	}
}

func TestResizeToMaxWidth_Downscale(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2000, 1000))
	resized, err := ResizeToMaxWidth(img, 1000)
	if err != nil {
		t.Fatalf("ResizeToMaxWidth returned error: %v", err)
	}
	if got := resized.Bounds().Size(); got.X != 1000 || got.Y != 500 {
		t.Fatalf("resized size mismatch: got=%dx%d want=1000x500", got.X, got.Y)
	}
}

func TestResizeToMaxWidth_InvalidWidth(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	_, err := ResizeToMaxWidth(img, 0)
	if err == nil {
		t.Fatal("expected error when maxWidth <= 0")
	}
}

func TestAddInnerPadding(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	fillRect(img, 0, 0, 20, 10, color.NRGBA{R: 255, G: 255, B: 255, A: 255})

	padded, err := AddInnerPadding(img, 10)
	if err != nil {
		t.Fatalf("AddInnerPadding returned error: %v", err)
	}

	if got := padded.Bounds().Size(); got.X != 40 || got.Y != 30 {
		t.Fatalf("padded size mismatch: got=%dx%d want=40x30", got.X, got.Y)
	}
	if c := color.NRGBAModel.Convert(padded.At(0, 0)).(color.NRGBA); c.A != 0 {
		t.Fatalf("expected transparent border pixel, got alpha=%d", c.A)
	}
	if c := color.NRGBAModel.Convert(padded.At(10, 10)).(color.NRGBA); c.R != 255 || c.G != 255 || c.B != 255 || c.A != 255 {
		t.Fatalf("expected original image at padded offset, got=%+v", c)
	}
}

func TestAddInnerPadding_InvalidPadding(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	_, err := AddInnerPadding(img, -1)
	if err == nil {
		t.Fatal("expected error when padding < 0")
	}
}

func TestForceWhiteForeground_OnlyChangesNonTransparentPixels(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 0})
	img.SetNRGBA(1, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 128})
	img.SetNRGBA(2, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})

	out := ForceWhiteForeground(img)

	if c := color.NRGBAModel.Convert(out.At(0, 0)).(color.NRGBA); c != (color.NRGBA{R: 10, G: 20, B: 30, A: 0}) {
		t.Fatalf("transparent pixel should remain unchanged, got=%+v", c)
	}
	if c := color.NRGBAModel.Convert(out.At(1, 0)).(color.NRGBA); c != (color.NRGBA{R: 255, G: 255, B: 255, A: 128}) {
		t.Fatalf("semi-transparent pixel mismatch, got=%+v", c)
	}
	if c := color.NRGBAModel.Convert(out.At(2, 0)).(color.NRGBA); c != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Fatalf("opaque pixel mismatch, got=%+v", c)
	}
}

func TestToNRGBA_FastPathNRGBA_SubImageCloneAndNormalize(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 6, 4))
	src.SetNRGBA(2, 1, color.NRGBA{R: 10, G: 20, B: 30, A: 40})
	src.SetNRGBA(4, 2, color.NRGBA{R: 100, G: 110, B: 120, A: 130})

	sub := src.SubImage(image.Rect(2, 1, 5, 3))
	out := toNRGBA(sub)

	if got := out.Bounds(); got != image.Rect(0, 0, 3, 2) {
		t.Fatalf("normalized bounds mismatch: got=%v want=%v", got, image.Rect(0, 0, 3, 2))
	}
	if got := out.NRGBAAt(0, 0); got != (color.NRGBA{R: 10, G: 20, B: 30, A: 40}) {
		t.Fatalf("pixel copy mismatch at (0,0): got=%+v", got)
	}
	if got := out.NRGBAAt(2, 1); got != (color.NRGBA{R: 100, G: 110, B: 120, A: 130}) {
		t.Fatalf("pixel copy mismatch at (2,1): got=%+v", got)
	}

	// Ensure clone semantics: changing source after conversion must not mutate output.
	src.SetNRGBA(2, 1, color.NRGBA{R: 200, G: 201, B: 202, A: 203})
	if got := out.NRGBAAt(0, 0); got != (color.NRGBA{R: 10, G: 20, B: 30, A: 40}) {
		t.Fatalf("output should be independent clone, got=%+v", got)
	}
}

func TestToNRGBA_FastPathRGBAOpaque_SubImageCopy(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 5, 3))
	src.SetRGBA(1, 1, color.RGBA{R: 11, G: 22, B: 33, A: 255})
	src.SetRGBA(3, 2, color.RGBA{R: 44, G: 55, B: 66, A: 255})

	sub := src.SubImage(image.Rect(1, 1, 4, 3))
	out := toNRGBA(sub)

	if got := out.Bounds(); got != image.Rect(0, 0, 3, 2) {
		t.Fatalf("normalized bounds mismatch: got=%v want=%v", got, image.Rect(0, 0, 3, 2))
	}
	if got := out.NRGBAAt(0, 0); got != (color.NRGBA{R: 11, G: 22, B: 33, A: 255}) {
		t.Fatalf("pixel mismatch at (0,0): got=%+v", got)
	}
	if got := out.NRGBAAt(2, 1); got != (color.NRGBA{R: 44, G: 55, B: 66, A: 255}) {
		t.Fatalf("pixel mismatch at (2,1): got=%+v", got)
	}
}

func TestToNRGBA_RGBAUnpremultiplyAndAlphaEdges(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 1))
	src.Pix = []uint8{
		0, 0, 0, 0, // alpha=0 edge case must stay all zero
		50, 100, 150, 255, // alpha=255 should be copied directly
		64, 32, 16, 128, // semi-transparent pre-multiplied bytes
	}

	out := toNRGBA(src)
	if got := out.NRGBAAt(0, 0); got != (color.NRGBA{R: 0, G: 0, B: 0, A: 0}) {
		t.Fatalf("alpha=0 conversion mismatch: got=%+v", got)
	}
	if got := out.NRGBAAt(1, 0); got != (color.NRGBA{R: 50, G: 100, B: 150, A: 255}) {
		t.Fatalf("alpha=255 conversion mismatch: got=%+v", got)
	}
	if got := out.NRGBAAt(2, 0); got != (color.NRGBA{R: 127, G: 63, B: 31, A: 128}) {
		t.Fatalf("alpha<255 conversion mismatch: got=%+v", got)
	}
}

func TestToNRGBA_GenericPathAndZeroSizedInput(t *testing.T) {
	gray := image.NewGray(image.Rect(0, 0, 2, 1))
	gray.SetGray(0, 0, color.Gray{Y: 17})
	gray.SetGray(1, 0, color.Gray{Y: 240})

	out := toNRGBA(gray)
	if got := out.NRGBAAt(0, 0); got != (color.NRGBA{R: 17, G: 17, B: 17, A: 255}) {
		t.Fatalf("gray conversion mismatch at (0,0): got=%+v", got)
	}
	if got := out.NRGBAAt(1, 0); got != (color.NRGBA{R: 240, G: 240, B: 240, A: 255}) {
		t.Fatalf("gray conversion mismatch at (1,0): got=%+v", got)
	}

	zero := image.NewNRGBA(image.Rect(0, 0, 0, 0))
	zeroOut := toNRGBA(zero)
	if got := zeroOut.Bounds(); got != image.Rect(0, 0, 0, 0) {
		t.Fatalf("zero-sized conversion bounds mismatch: got=%v", got)
	}
}

func TestAutocropNonTransparent_NoVisiblePixelsReturnsConvertedImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 99, G: 88, B: 77, A: 0})
		}
	}

	out := AutocropNonTransparent(src, true)
	if got := out.Bounds(); got != image.Rect(0, 0, 4, 3) {
		t.Fatalf("expected unchanged bounds for all-transparent input, got=%v", got)
	}
	if got := out.NRGBAAt(1, 1); got != (color.NRGBA{R: 0, G: 0, B: 0, A: 0}) {
		t.Fatalf("unexpected pixel after conversion: got=%+v", got)
	}
}

func fillRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}
