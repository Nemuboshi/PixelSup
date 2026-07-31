package compose

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"pixelsup-go/internal/model"
)

var (
	black = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	white = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	// Reuse uniform sources for the hot-path background/separator fills.
	blackUniform = image.NewUniform(black)
	whiteUniform = image.NewUniform(white)
)

var (
	vectorFontOnce sync.Once
	vectorFontErr  error
	vectorTTF      *opentype.Font
)

// ProgressFunc receives completion events after each output sheet is produced.
// done and total are one-based counters mirroring the Python callback contract.
type ProgressFunc func(done, total int)

// PlacedSheet represents one composed sheet image and the cue indexes included in that file.
type PlacedSheet struct {
	Name       string
	Image      *image.RGBA
	CueIndexes []int
}

// ComposeSheetsWithDigitSeparator stacks cues vertically and inserts a synthetic
// separator row containing "0123456789" between adjacent cues.
//
// Unlike ComposeSheets, this mode does not draw per-row index gutters. Cue
// placement metadata remains identical: PositionInSheet counts only real cues.
// separatorHeight acts as a lower bound for each separator row. Actual separator
// height is unified per sheet as max(separatorHeight, min cue height in chunk).
func ComposeSheetsWithDigitSeparator(
	cues []model.RenderedCue,
	limit int,
	separatorHeight int,
	progress ProgressFunc,
) ([]PlacedSheet, map[int]model.CuePlacement, error) {
	if limit <= 0 {
		return nil, nil, fmt.Errorf("limit must be > 0")
	}
	if separatorHeight < 0 {
		return nil, nil, fmt.Errorf("separatorHeight must be >= 0")
	}

	sheets := make([]PlacedSheet, 0, (len(cues)+limit-1)/limit)
	placements := make(map[int]model.CuePlacement, len(cues))
	if len(cues) == 0 {
		return sheets, placements, nil
	}

	totalSheets := (len(cues) + limit - 1) / limit

	for sheetIdx, start := 1, 0; start < len(cues); sheetIdx, start = sheetIdx+1, start+limit {
		end := start + limit
		if end > len(cues) {
			end = len(cues)
		}
		chunk := cues[start:end]

		maxWidth := 0
		totalHeight := 0
		minCueHeight := 0
		for _, cue := range chunk {
			if cue.Frame == nil {
				return nil, nil, fmt.Errorf("cue index %d has nil frame", cue.Cue.Index)
			}
			bounds := cue.Frame.Bounds()
			w := bounds.Dx()
			h := bounds.Dy()
			if w <= 0 || h <= 0 {
				return nil, nil, fmt.Errorf("cue index %d has invalid frame size %dx%d", cue.Cue.Index, w, h)
			}
			if w > maxWidth {
				maxWidth = w
			}
			totalHeight += h
			if minCueHeight == 0 || h < minCueHeight {
				minCueHeight = h
			}
		}
		separatorRowHeight := minCueHeight
		if separatorRowHeight < separatorHeight {
			separatorRowHeight = separatorHeight
		}
		totalHeight += separatorRowHeight * len(chunk)

		canvas := image.NewRGBA(image.Rect(0, 0, maxWidth, totalHeight))
		fillRect(canvas, canvas.Bounds(), black)

		cueIndexes := make([]int, 0, len(chunk))
		y := 0
		for pos, cue := range chunk {
			frameBounds := cue.Frame.Bounds()
			w := frameBounds.Dx()
			h := frameBounds.Dy()

			x := (maxWidth - w) / 2
			dst := image.Rect(x, y, x+w, y+h)
			draw.Draw(canvas, dst, cue.Frame, frameBounds.Min, draw.Over)

			positionInSheet := pos + 1
			sheetName := fmt.Sprintf("sheet_%04d.png", sheetIdx)
			placements[cue.Cue.Index] = model.CuePlacement{SheetName: sheetName, PositionInSheet: positionInSheet}
			cueIndexes = append(cueIndexes, cue.Cue.Index)

			y += h
			sepRect := image.Rect(0, y, canvas.Bounds().Dx(), y+separatorRowHeight)
			drawDigitSeparatorRow(canvas, sepRect)
			y += separatorRowHeight
		}

		sheets = append(sheets, PlacedSheet{
			Name:       fmt.Sprintf("sheet_%04d.png", sheetIdx),
			Image:      canvas,
			CueIndexes: cueIndexes,
		})
		if progress != nil {
			progress(sheetIdx, totalSheets)
		}
	}

	return sheets, placements, nil
}

// fillRect paints a solid color in RGBA space with explicit clipping.
func fillRect(dst *image.RGBA, rect image.Rectangle, c color.RGBA) {
	r := rect.Intersect(dst.Bounds())
	if r.Empty() {
		return
	}
	// draw.Src writes the solid color directly and avoids per-pixel SetRGBA overhead.
	src := image.Image(blackUniform)
	switch c {
	case black:
		src = blackUniform
	case white:
		src = whiteUniform
	default:
		src = image.NewUniform(c)
	}
	draw.Draw(dst, r, src, image.Point{}, draw.Src)
}

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
