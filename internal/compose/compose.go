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

// ComposeSheets reproduces pixelsup/compose.py behavior in Go.
//
// The function chunks cues by limit, stacks each chunk vertically, and returns both
// rendered sheet images and per-cue placement metadata used by timeline/mapping writers.
// If showRowIndex is true, a left gutter is reserved for numeric row labels and an optional
// white separator column is inserted when padding > 0.
func ComposeSheets(
	cues []model.RenderedCue,
	limit int,
	padding int,
	showRowIndex bool,
	progress ProgressFunc,
) ([]PlacedSheet, map[int]model.CuePlacement, error) {
	if limit <= 0 {
		return nil, nil, fmt.Errorf("limit must be > 0")
	}
	if padding < 0 {
		return nil, nil, fmt.Errorf("padding must be >= 0")
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
		for i, cue := range chunk {
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
			if i == 0 || h < minCueHeight {
				minCueHeight = h
			}
		}
		totalHeight += padding * (len(chunk) - 1)

		gutterWidth := 0
		separatorWidth := 0
		indexBoxSize := 0
		if showRowIndex {
			// Python uses the smallest cue height in this sheet so every label fits each row.
			// A lower bound of 10px keeps single-digit labels legible on short cues.
			indexBoxSize = minCueHeight
			if indexBoxSize < 10 {
				indexBoxSize = 10
			}
			gutterWidth = indexBoxSize
			if padding > 0 {
				separatorWidth = padding
			}
		}

		// Canvas width mirrors Python layout math exactly:
		// [optional index gutter][optional white separator][centered subtitle content].
		canvas := image.NewRGBA(image.Rect(0, 0, gutterWidth+separatorWidth+maxWidth, totalHeight))
		fillRect(canvas, canvas.Bounds(), black)

		subtitleXBase := gutterWidth + separatorWidth
		if separatorWidth > 0 {
			sep := image.Rect(gutterWidth, 0, gutterWidth+separatorWidth, totalHeight)
			fillRect(canvas, sep, white)
		}

		cueIndexes := make([]int, 0, len(chunk))
		y := 0
		for pos, cue := range chunk {
			frameBounds := cue.Frame.Bounds()
			w := frameBounds.Dx()
			h := frameBounds.Dy()

			// Each cue is horizontally centered relative to the widest cue in the chunk.
			x := subtitleXBase + (maxWidth-w)/2
			dst := image.Rect(x, y, x+w, y+h)
			draw.Draw(canvas, dst, cue.Frame, frameBounds.Min, draw.Over)

			if showRowIndex {
				drawRowIndexLabel(canvas, cue.Cue.Index, image.Rect(0, y, gutterWidth, y+h), indexBoxSize, separatorWidth)
			}

			positionInSheet := pos + 1
			sheetName := fmt.Sprintf("sheet_%04d.png", sheetIdx)
			placements[cue.Cue.Index] = model.CuePlacement{SheetName: sheetName, PositionInSheet: positionInSheet}
			cueIndexes = append(cueIndexes, cue.Cue.Index)

			y += h
			if padding > 0 && positionInSheet < len(chunk) {
				// Inter-row spacing is a full-width white stripe in Python; keep that contract.
				fillRect(canvas, image.Rect(0, y, canvas.Bounds().Dx(), y+padding), white)
				y += padding
			}
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

// drawRowIndexLabel renders decimal indexes using an embedded digit-only bitmap font.
//
// The font intentionally contains only 0-9 glyphs so it stays tiny and fully
// deterministic across platforms (no system-font dependency).
// Glyphs are 5x7 pixel cells and are uniformly scaled to fit indexBoxSize.
func drawRowIndexLabel(dst *image.RGBA, index int, rowRect image.Rectangle, indexBoxSize int, separatorWidth int) {
	label := fmt.Sprintf("%d", index)
	if len(label) == 0 || indexBoxSize <= 0 || rowRect.Dx() <= 0 || rowRect.Dy() <= 0 {
		return
	}

	// Prefer vector glyph rendering for OCR readability.
	// If vector font init/render fails for any reason, fall back to deterministic bitmap digits.
	if drawVectorIndexLabel(dst, label, rowRect, indexBoxSize, separatorWidth) {
		return
	}

	const glyphW = 5
	const glyphH = 7
	fitW := indexBoxSize - separatorWidth
	if fitW <= 0 {
		return
	}

	scale := indexBoxSize / glyphH
	if scale < 1 {
		scale = 1
	}
	digitWidth := glyphW * scale
	gap := max(1, scale/2)
	textWidth := len(label)*digitWidth + (len(label)-1)*gap
	textHeight := glyphH * scale

	if textWidth > fitW-2 {
		usable := fitW - 2
		if usable <= 0 {
			return
		}
		scale = usable / (len(label)*glyphW + (len(label) - 1))
		if scale < 1 {
			scale = 1
		}
		digitWidth = glyphW * scale
		gap = max(1, scale/2)
		textWidth = len(label)*digitWidth + (len(label)-1)*gap
		textHeight = glyphH * scale
	}

	boxY := rowRect.Min.Y + max(0, (rowRect.Dy()-indexBoxSize)/2)
	x := rowRect.Min.X + max(0, (fitW-textWidth)/2)
	y := boxY + max(0, (indexBoxSize-textHeight)/2)

	for i := 0; i < len(label); i++ {
		ch := label[i]
		if ch < '0' || ch > '9' {
			continue
		}
		drawBitmapDigit(dst, ch-'0', x+i*(digitWidth+gap), y, scale)
	}
}

func initVectorFont() {
	vectorFontOnce.Do(func() {
		vectorTTF, vectorFontErr = opentype.Parse(goregular.TTF)
	})
}

func drawVectorIndexLabel(dst *image.RGBA, label string, rowRect image.Rectangle, indexBoxSize int, separatorWidth int) bool {
	initVectorFont()
	if vectorFontErr != nil || vectorTTF == nil {
		return false
	}

	boxY := rowRect.Min.Y + max(0, (rowRect.Dy()-indexBoxSize)/2)
	boxX := rowRect.Min.X
	fitW := indexBoxSize - separatorWidth
	if fitW <= 0 {
		return false
	}

	// Apply symmetric horizontal padding after subtracting separator width.
	innerPadX := max(2, int(float64(fitW)*0.12))
	if innerPadX*2 >= fitW {
		innerPadX = max(1, fitW/4)
	}
	topPad := max(2, int(float64(indexBoxSize)*0.12))
	bottomPad := topPad
	availW := fitW - innerPadX*2
	availH := indexBoxSize - topPad - bottomPad
	if availW <= 0 || availH <= 0 {
		return false
	}

	fontSize := float64(indexBoxSize) * 0.82
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

		// BoundString reflects actual glyph ink bounds better than advance/metrics.
		bounds, _ := font.BoundString(face, label)
		textW := (bounds.Max.X - bounds.Min.X).Round()
		textH := (bounds.Max.Y - bounds.Min.Y).Round()
		if textW <= availW && textH <= availH {
			// Dot is baseline origin. Offset by -bounds.Min to place ink box at target.
			targetX := boxX + innerPadX + max(0, (availW-textW)/2)
			targetY := boxY + topPad + max(0, (availH-textH)/2)
			dotX := targetX - bounds.Min.X.Round()
			dotY := targetY - bounds.Min.Y.Round()

			d := &font.Drawer{
				Dst:  dst,
				Src:  image.NewUniform(white),
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

func drawBitmapDigit(dst *image.RGBA, digit byte, x, y, scale int) {
	glyph := digitGlyphs[digit]
	for gy := 0; gy < len(glyph); gy++ {
		for gx := 0; gx < len(glyph[gy]); gx++ {
			if glyph[gy][gx] == 0 {
				continue
			}
			fillRect(dst, image.Rect(x+gx*scale, y+gy*scale, x+(gx+1)*scale, y+(gy+1)*scale), white)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
