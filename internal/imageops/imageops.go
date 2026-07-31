package imageops

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// AutocropNonTransparent removes transparent borders using the alpha channel.
//
// Behavior intentionally mirrors the Python reference implementation:
//  1. Convert the input image to RGBA-like data (NRGBA in Go for straight alpha).
//  2. Find the bounding box of pixels whose alpha is non-zero.
//  3. If no non-transparent pixels exist, return the converted image unchanged.
//  4. If the alpha box is smaller than the full image, crop to that box.
//  5. If the alpha box is full-image and solidBgFallback is true, attempt a
//     second crop pass based on RGB difference from a solid corner color.
//
// The fallback path exists for fully opaque source images where alpha-based
// cropping cannot remove borders (for example, RGB subtitles on a solid matte).
func AutocropNonTransparent(src image.Image, solidBgFallback bool) *image.NRGBA {
	rgba := toNRGBA(src)
	bbox, ok := alphaBoundingBox(rgba)
	if !ok {
		return rgba
	}

	full := image.Rect(0, 0, rgba.Bounds().Dx(), rgba.Bounds().Dy())
	if bbox != full {
		return cropNRGBA(rgba, bbox)
	}
	if !solidBgFallback {
		return rgba
	}

	return autocropSolidBackground(rgba)
}

// ResizeToMaxWidth scales down an image so its width is at most maxWidth while
// preserving aspect ratio.
//
// The function never scales up smaller images and always returns NRGBA output.
// For deterministic behavior we use bilinear resampling implemented with stdlib
// primitives only, avoiding external dependencies.
func ResizeToMaxWidth(src image.Image, maxWidth int) (*image.NRGBA, error) {
	if maxWidth <= 0 {
		return nil, fmt.Errorf("max_width must be > 0")
	}

	rgba := toNRGBA(src)
	width := rgba.Bounds().Dx()
	height := rgba.Bounds().Dy()
	if width <= maxWidth {
		return rgba, nil
	}

	scale := float64(maxWidth) / float64(width)
	newHeight := int(math.Round(float64(height) * scale))
	if newHeight < 1 {
		newHeight = 1
	}

	return resizeBilinearNRGBA(rgba, maxWidth, newHeight), nil
}

// AddInnerPadding places the image onto a larger transparent canvas with equal
// padding on all sides. A padding of zero returns the converted image directly.
func AddInnerPadding(src image.Image, padding int) (*image.NRGBA, error) {
	if padding < 0 {
		return nil, fmt.Errorf("padding must be >= 0")
	}

	rgba := toNRGBA(src)
	if padding == 0 {
		return rgba, nil
	}

	w := rgba.Bounds().Dx()
	h := rgba.Bounds().Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w+padding*2, h+padding*2))

	for y := 0; y < h; y++ {
		srcStart := rgba.PixOffset(0, y)
		dstStart := out.PixOffset(padding, y+padding)
		copy(out.Pix[dstStart:dstStart+w*4], rgba.Pix[srcStart:srcStart+w*4])
	}

	return out, nil
}

// ForceWhiteForeground rewrites every non-transparent pixel to white while
// preserving each pixel's original alpha value.
//
// This keeps antialias edges intact because transparency is untouched, while
// normalizing glyph color to pure white for downstream OCR/composition parity.
func ForceWhiteForeground(src image.Image) *image.NRGBA {
	rgba := toNRGBA(src)
	for i := 0; i < len(rgba.Pix); i += 4 {
		a := rgba.Pix[i+3]
		if a == 0 {
			continue
		}
		rgba.Pix[i+0] = 255
		rgba.Pix[i+1] = 255
		rgba.Pix[i+2] = 255
	}
	return rgba
}

// autocropSolidBackground performs the fallback crop used when alpha covers the
// entire frame. It only activates when all four corner pixels are identical,
// which is a practical signal that borders likely share one matte color.
func autocropSolidBackground(img *image.NRGBA) *image.NRGBA {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	if w == 0 || h == 0 {
		return img
	}

	corners := [4]color.NRGBA{
		img.NRGBAAt(0, 0),
		img.NRGBAAt(w-1, 0),
		img.NRGBAAt(0, h-1),
		img.NRGBAAt(w-1, h-1),
	}
	for i := 1; i < len(corners); i++ {
		if corners[i] != corners[0] {
			return img
		}
	}

	bg := corners[0]
	bbox, ok := solidDifferenceBoundingBox(img, bg)
	if !ok {
		return img
	}
	return cropNRGBA(img, bbox)
}

// solidDifferenceBoundingBox returns the minimal rectangle containing every
// pixel whose RGB differs from the provided background color. Alpha is ignored
// to mirror PIL's diff.convert("RGB").getbbox() logic from the Python version.
func solidDifferenceBoundingBox(img *image.NRGBA, bg color.NRGBA) (image.Rectangle, bool) {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()

	minX, minY := w, h
	maxX, maxY := -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := img.NRGBAAt(x, y)
			if p.R == bg.R && p.G == bg.G && p.B == bg.B {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}

	if maxX < minX || maxY < minY {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

// alphaBoundingBox returns the minimal rectangle containing all pixels with
// alpha > 0 in an NRGBA image.
func alphaBoundingBox(img *image.NRGBA) (image.Rectangle, bool) {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()

	minX, minY := w, h
	maxX, maxY := -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}

	if maxX < minX || maxY < minY {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

// cropNRGBA copies a rectangular region into a new origin-normalized image.
// The returned image always has bounds (0,0)-(w,h), matching PIL crop output.
func cropNRGBA(img *image.NRGBA, rect image.Rectangle) *image.NRGBA {
	r := rect.Intersect(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	if r.Empty() {
		return image.NewNRGBA(image.Rect(0, 0, 0, 0))
	}

	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		srcStart := img.PixOffset(r.Min.X, r.Min.Y+y)
		dstStart := out.PixOffset(0, y)
		copy(out.Pix[dstStart:dstStart+r.Dx()*4], img.Pix[srcStart:srcStart+r.Dx()*4])
	}
	return out
}

// toNRGBA converts any source image into an origin-normalized NRGBA image.
// A clone is always returned so callers can mutate output safely.
func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if w == 0 || h == 0 {
		return out
	}

	switch s := src.(type) {
	case *image.NRGBA:
		// Keep clone semantics (callers may mutate), but copy rows directly.
		for y := 0; y < h; y++ {
			srcStart := s.PixOffset(b.Min.X, b.Min.Y+y)
			dstStart := out.PixOffset(0, y)
			copy(out.Pix[dstStart:dstStart+w*4], s.Pix[srcStart:srcStart+w*4])
		}
		return out
	case *image.RGBA:
		// RGBA stores pre-multiplied channels; convert once from raw bytes.
		// If fully opaque, bytes are already directly compatible with NRGBA.
		if s.Opaque() {
			for y := 0; y < h; y++ {
				srcStart := s.PixOffset(b.Min.X, b.Min.Y+y)
				dstStart := out.PixOffset(0, y)
				copy(out.Pix[dstStart:dstStart+w*4], s.Pix[srcStart:srcStart+w*4])
			}
			return out
		}

		for y := 0; y < h; y++ {
			srcStart := s.PixOffset(b.Min.X, b.Min.Y+y)
			dstStart := out.PixOffset(0, y)
			srcRow := s.Pix[srcStart : srcStart+w*4]
			dstRow := out.Pix[dstStart : dstStart+w*4]
			for i := 0; i < len(srcRow); i += 4 {
				pr := srcRow[i+0]
				pg := srcRow[i+1]
				pb := srcRow[i+2]
				a := srcRow[i+3]

				if a == 0 {
					dstRow[i+0], dstRow[i+1], dstRow[i+2], dstRow[i+3] = 0, 0, 0, 0
					continue
				}
				if a == 0xff {
					dstRow[i+0], dstRow[i+1], dstRow[i+2], dstRow[i+3] = pr, pg, pb, a
					continue
				}

				// Match color.NRGBAModel conversion behavior for pre-multiplied RGBA input.
				aa := uint32(a)
				dstRow[i+0] = uint8(((uint32(pr) * 0xffff) / aa) >> 8)
				dstRow[i+1] = uint8(((uint32(pg) * 0xffff) / aa) >> 8)
				dstRow[i+2] = uint8(((uint32(pb) * 0xffff) / aa) >> 8)
				dstRow[i+3] = a
			}
		}
		return out
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.SetNRGBA(x, y, color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA))
		}
	}
	return out
}

// resizeBilinearNRGBA rescales an image with bilinear interpolation.
//
// For each destination pixel center, we map back to source space and blend the
// surrounding 2x2 source pixels proportionally. Clamping at edges avoids out-of-
// range access and matches expected behavior when sampling near image borders.
func resizeBilinearNRGBA(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	if dstW <= 0 || dstH <= 0 {
		return image.NewNRGBA(image.Rect(0, 0, 0, 0))
	}

	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	if srcW == 0 || srcH == 0 {
		return image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	}

	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	scaleX := float64(srcW) / float64(dstW)
	scaleY := float64(srcH) / float64(dstH)

	for y := 0; y < dstH; y++ {
		sy := (float64(y)+0.5)*scaleY - 0.5
		y0 := int(math.Floor(sy))
		y1 := y0 + 1
		wy := sy - float64(y0)

		if y0 < 0 {
			y0 = 0
		}
		if y1 >= srcH {
			y1 = srcH - 1
		}

		for x := 0; x < dstW; x++ {
			sx := (float64(x)+0.5)*scaleX - 0.5
			x0 := int(math.Floor(sx))
			x1 := x0 + 1
			wx := sx - float64(x0)

			if x0 < 0 {
				x0 = 0
			}
			if x1 >= srcW {
				x1 = srcW - 1
			}

			p00 := src.NRGBAAt(x0, y0)
			p10 := src.NRGBAAt(x1, y0)
			p01 := src.NRGBAAt(x0, y1)
			p11 := src.NRGBAAt(x1, y1)

			dst.SetNRGBA(x, y, color.NRGBA{
				R: bilinearChannel(p00.R, p10.R, p01.R, p11.R, wx, wy),
				G: bilinearChannel(p00.G, p10.G, p01.G, p11.G, wx, wy),
				B: bilinearChannel(p00.B, p10.B, p01.B, p11.B, wx, wy),
				A: bilinearChannel(p00.A, p10.A, p01.A, p11.A, wx, wy),
			})
		}
	}

	return dst
}

// bilinearChannel blends one channel from a 2x2 neighborhood.
func bilinearChannel(c00, c10, c01, c11 uint8, wx, wy float64) uint8 {
	v00 := float64(c00)
	v10 := float64(c10)
	v01 := float64(c01)
	v11 := float64(c11)

	top := v00 + (v10-v00)*wx
	bottom := v01 + (v11-v01)*wx
	value := top + (bottom-top)*wy
	if value < 0 {
		value = 0
	}
	if value > 255 {
		value = 255
	}
	return uint8(math.Round(value))
}
