package compose

import (
	"image"
	"image/color"
	"testing"

	"pixelsup-go/internal/model"
)

func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func cue(index, w, h int) model.RenderedCue {
	return model.RenderedCue{
		Cue:   model.SubtitleCue{Index: index, StartMS: (index - 1) * 1000, EndMS: index * 1000},
		Frame: solidRGBA(w, h, color.RGBA{R: 255, G: 255, B: 255, A: 255}),
	}
}

func TestComposeSheetsWithDigitSeparatorKeepsCuePlacements(t *testing.T) {
	cues := []model.RenderedCue{cue(1, 12, 4), cue(2, 12, 4), cue(3, 12, 4)}

	sheets, placements, err := ComposeSheetsWithDigitSeparator(cues, 10, 6, nil)
	if err != nil {
		t.Fatalf("ComposeSheetsWithDigitSeparator() error = %v", err)
	}
	if len(sheets) != 1 {
		t.Fatalf("len(sheets) = %d, want 1", len(sheets))
	}
	if got, want := placements[1].PositionInSheet, 1; got != want {
		t.Fatalf("placements[1].PositionInSheet = %d, want %d", got, want)
	}
	if got, want := placements[2].PositionInSheet, 2; got != want {
		t.Fatalf("placements[2].PositionInSheet = %d, want %d", got, want)
	}
	if got, want := placements[3].PositionInSheet, 3; got != want {
		t.Fatalf("placements[3].PositionInSheet = %d, want %d", got, want)
	}

	// Separator height is unified per sheet: max(param=6, min cue height=4), so 6.
	// Total height includes a trailing separator row: 4 + 6 + 4 + 6 + 4 + 6.
	if got, want := sheets[0].Image.Bounds().Dy(), 30; got != want {
		t.Fatalf("sheet height = %d, want %d", got, want)
	}

	// Separator row contains black digits over white background.
	// Assert that at least one white background pixel exists in the band.
	foundWhite := false
	for x := 0; x < sheets[0].Image.Bounds().Dx(); x++ {
		px := color.RGBAModel.Convert(sheets[0].Image.At(x, 5)).(color.RGBA)
		if px.R == 255 && px.G == 255 && px.B == 255 {
			foundWhite = true
			break
		}
	}
	if !foundWhite {
		t.Fatalf("expected white background pixels in separator row")
	}

	foundBottomWhite := false
	lastSeparatorY := sheets[0].Image.Bounds().Dy() - 1
	for x := 0; x < sheets[0].Image.Bounds().Dx(); x++ {
		px := color.RGBAModel.Convert(sheets[0].Image.At(x, lastSeparatorY)).(color.RGBA)
		if px.R == 255 && px.G == 255 && px.B == 255 {
			foundBottomWhite = true
			break
		}
	}
	if !foundBottomWhite {
		t.Fatalf("expected white background pixels in trailing separator row")
	}
}

func TestComposeSheetsWithDigitSeparatorUsesMinCueHeightPerSheet(t *testing.T) {
	cues := []model.RenderedCue{cue(1, 40, 30), cue(2, 40, 20)}

	sheets, _, err := ComposeSheetsWithDigitSeparator(cues, 10, 5, nil)
	if err != nil {
		t.Fatalf("ComposeSheetsWithDigitSeparator() error = %v", err)
	}
	// Separator is max(param=5, min cue height=20) => 20.
	// Total height includes a trailing separator row: 30 + 20 + 20 + 20.
	if got, want := sheets[0].Image.Bounds().Dy(), 90; got != want {
		t.Fatalf("sheet height = %d, want %d", got, want)
	}
}
