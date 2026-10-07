package compose

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"pixelsup-go/internal/model"
)

var (
	black = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	white = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	// Reuse uniform sources for the hot-path background/separator fills.
	blackUniform = image.NewUniform(black)
	whiteUniform = image.NewUniform(white)
)

type ProgressFunc func(done, total int)

// PlacedSheet represents one composed sheet image and the cue indexes included in that file.
type PlacedSheet struct {
	Name       string
	Image      *image.RGBA
	CueIndexes []int
}

type SheetLayout string

const (
	LayoutDigits     SheetLayout = "digits"
	LayoutNoRowIndex SheetLayout = "no-row-index"
)

// ComposeSheets stacks cues into OCR sheets using either a digit separator or
// plain white row gaps without an index gutter.
func ComposeSheets(
	cues []model.RenderedCue,
	limit int,
	separatorHeight int,
	layout SheetLayout,
	progress ProgressFunc,
) ([]PlacedSheet, map[int]model.CuePlacement, error) {
	if limit <= 0 {
		return nil, nil, fmt.Errorf("limit must be > 0")
	}
	if separatorHeight < 0 {
		return nil, nil, fmt.Errorf("separatorHeight must be >= 0")
	}
	if layout != LayoutDigits && layout != LayoutNoRowIndex {
		return nil, nil, fmt.Errorf("unknown sheet layout %q", layout)
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

		rowGap := separatorHeight
		if layout == LayoutDigits && rowGap < minCueHeight {
			rowGap = minCueHeight
		}
		if layout == LayoutDigits {
			totalHeight += rowGap * len(chunk)
		} else {
			totalHeight += rowGap * (len(chunk) - 1)
		}

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
			if layout == LayoutDigits {
				sepRect := image.Rect(0, y, canvas.Bounds().Dx(), y+rowGap)
				drawDigitSeparatorRow(canvas, sepRect)
				y += rowGap
			} else if pos+1 < len(chunk) {
				fillRect(canvas, image.Rect(0, y, canvas.Bounds().Dx(), y+rowGap), white)
				y += rowGap
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

// ComposeSheetsWithDigitSeparator keeps the original digits-composed API.
func ComposeSheetsWithDigitSeparator(
	cues []model.RenderedCue,
	limit int,
	separatorHeight int,
	progress ProgressFunc,
) ([]PlacedSheet, map[int]model.CuePlacement, error) {
	return ComposeSheets(cues, limit, separatorHeight, LayoutDigits, progress)
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
