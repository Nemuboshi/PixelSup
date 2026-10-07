package compose

import (
	"image"
	"image/color"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	vectorFontOnce sync.Once
	vectorFontErr  error
	vectorTTF      *opentype.Font
)

func drawDigitSeparatorRow(dst *image.RGBA, rowRect image.Rectangle) {
	if rowRect.Empty() {
		return
	}
	fillRect(dst, rowRect, white)
	if drawVectorCenteredDigits(dst, "0123456789", rowRect, black) {
		return
	}

	const glyphW = 5
	const glyphH = 7
	text := "0123456789"
	scale := rowRect.Dy() / (glyphH + 2)
	if scale < 1 {
		scale = 1
	}
	digitWidth := glyphW * scale
	gap := max(1, scale/2)
	textWidth := len(text)*digitWidth + (len(text)-1)*gap
	textHeight := glyphH * scale

	if textWidth > rowRect.Dx()-2 {
		usable := rowRect.Dx() - 2
		if usable <= 0 {
			return
		}
		scale = usable / (len(text)*glyphW + (len(text) - 1))
		if scale < 1 {
			scale = 1
		}
		digitWidth = glyphW * scale
		gap = max(1, scale/2)
		textWidth = len(text)*digitWidth + (len(text)-1)*gap
		textHeight = glyphH * scale
	}

	x := rowRect.Min.X + max(0, (rowRect.Dx()-textWidth)/2)
	y := rowRect.Min.Y + max(0, (rowRect.Dy()-textHeight)/2)
	for i := 0; i < len(text); i++ {
		drawBitmapDigit(dst, text[i]-'0', x+i*(digitWidth+gap), y, scale, black)
	}
}

func drawVectorCenteredDigits(dst *image.RGBA, label string, rowRect image.Rectangle, fg color.RGBA) bool {
	initVectorFont()
	if vectorFontErr != nil || vectorTTF == nil || rowRect.Empty() {
		return false
	}

	paddingX := max(2, int(float64(rowRect.Dx())*0.04))
	paddingY := max(2, int(float64(rowRect.Dy())*0.18))
	availW := rowRect.Dx() - paddingX*2
	availH := rowRect.Dy() - paddingY*2
	if availW <= 0 || availH <= 0 {
		return false
	}

	fontSize := float64(rowRect.Dy()) * 0.7
	if fontSize < 8 {
		fontSize = 8
	}

	for attempt := 0; attempt < 10; attempt++ {
		face, err := opentype.NewFace(vectorTTF, &opentype.FaceOptions{
			Size:    fontSize,
			DPI:     72,
			Hinting: font.HintingFull,
		})
		if err != nil {
			return false
		}
		bounds, _ := font.BoundString(face, label)
		textW := (bounds.Max.X - bounds.Min.X).Round()
		textH := (bounds.Max.Y - bounds.Min.Y).Round()
		if textW <= availW && textH <= availH {
			targetX := rowRect.Min.X + paddingX + max(0, (availW-textW)/2)
			targetY := rowRect.Min.Y + paddingY + max(0, (availH-textH)/2)
			dotX := targetX - bounds.Min.X.Round()
			dotY := targetY - bounds.Min.Y.Round()

			d := &font.Drawer{
				Dst:  dst,
				Src:  image.NewUniform(fg),
				Face: face,
				Dot:  fixed.P(dotX, dotY),
			}
			d.DrawString(label)
			face.Close()
			return true
		}
		face.Close()
		fontSize *= 0.9
		if fontSize < 6 {
			break
		}
	}
	return false
}

func initVectorFont() {
	vectorFontOnce.Do(func() {
		vectorTTF, vectorFontErr = opentype.Parse(goregular.TTF)
	})
}

// digitGlyphs is a minimal embedded 5x7 bitmap font for 0-9.
// A value of 1 means the pixel is filled.
var digitGlyphs = [10][7][5]uint8{
	{ // 0
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 1, 1},
		{1, 0, 1, 0, 1},
		{1, 1, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	{ // 1
		{0, 0, 1, 0, 0},
		{0, 1, 1, 0, 0},
		{1, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{1, 1, 1, 1, 1},
	},
	{ // 2
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 1, 0},
		{0, 0, 1, 0, 0},
		{0, 1, 0, 0, 0},
		{1, 1, 1, 1, 1},
	},
	{ // 3
		{1, 1, 1, 1, 0},
		{0, 0, 0, 0, 1},
		{0, 0, 1, 1, 0},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	{ // 4
		{0, 0, 0, 1, 0},
		{0, 0, 1, 1, 0},
		{0, 1, 0, 1, 0},
		{1, 0, 0, 1, 0},
		{1, 1, 1, 1, 1},
		{0, 0, 0, 1, 0},
		{0, 0, 0, 1, 0},
	},
	{ // 5
		{1, 1, 1, 1, 1},
		{1, 0, 0, 0, 0},
		{1, 1, 1, 1, 0},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	{ // 6
		{0, 0, 1, 1, 0},
		{0, 1, 0, 0, 0},
		{1, 0, 0, 0, 0},
		{1, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	{ // 7
		{1, 1, 1, 1, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 1, 0},
		{0, 0, 1, 0, 0},
		{0, 1, 0, 0, 0},
		{0, 1, 0, 0, 0},
		{0, 1, 0, 0, 0},
	},
	{ // 8
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	{ // 9
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 1, 0},
		{0, 1, 1, 0, 0},
	},
}

func drawBitmapDigit(dst *image.RGBA, digit byte, x, y, scale int, fg color.RGBA) {
	glyph := digitGlyphs[digit]
	for gy := 0; gy < len(glyph); gy++ {
		for gx := 0; gx < len(glyph[gy]); gx++ {
			if glyph[gy][gx] == 0 {
				continue
			}
			fillRect(dst, image.Rect(x+gx*scale, y+gy*scale, x+(gx+1)*scale, y+(gy+1)*scale), fg)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
