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

func TestComposeSheetsPlacementMapping(t *testing.T) {
	cues := make([]model.RenderedCue, 0, 6)
	for i := 1; i <= 6; i++ {
		cues = append(cues, cue(i, 12, 5))
	}

	sheets, placements, err := ComposeSheets(cues, 4, 2, true, nil)
	if err != nil {
		t.Fatalf("ComposeSheets() error = %v", err)
	}
	if len(sheets) != 2 {
		t.Fatalf("len(sheets) = %d, want 2", len(sheets))
	}
	if got, want := sheets[0].CueIndexes, []int{1, 2, 3, 4}; len(got) != len(want) || got[0] != 1 || got[3] != 4 {
		t.Fatalf("first sheet cue indexes = %v, want %v", got, want)
	}
	if got, want := placements[1].SheetName, "sheet_0001.png"; got != want {
		t.Fatalf("placements[1].SheetName = %q, want %q", got, want)
	}
	if got, want := placements[4].PositionInSheet, 4; got != want {
		t.Fatalf("placements[4].PositionInSheet = %d, want %d", got, want)
	}
	if got, want := placements[5].SheetName, "sheet_0002.png"; got != want {
		t.Fatalf("placements[5].SheetName = %q, want %q", got, want)
	}
}

func TestComposeSheetsAppliesWhiteSpacingRows(t *testing.T) {
	cues := []model.RenderedCue{cue(1, 10, 4), cue(2, 10, 4)}

	sheets, _, err := ComposeSheets(cues, 20, 3, false, nil)
	if err != nil {
		t.Fatalf("ComposeSheets() error = %v", err)
	}
	if len(sheets) != 1 {
		t.Fatalf("len(sheets) = %d, want 1", len(sheets))
	}

	sheet := sheets[0].Image
	if got, want := sheet.Bounds().Dy(), 11; got != want {
		t.Fatalf("sheet height = %d, want %d", got, want)
	}
	for x := 0; x < sheet.Bounds().Dx(); x++ {
		if got := color.RGBAModel.Convert(sheet.At(x, 4)).(color.RGBA); got.R != 255 || got.G != 255 || got.B != 255 {
			t.Fatalf("spacing row pixel at x=%d = %+v, want white", x, got)
		}
	}
}

func TestComposeSheetsRowIndexToggleAndGutterSizing(t *testing.T) {
	cues := []model.RenderedCue{cue(7, 20, 10), cue(8, 20, 30)}

	withIndex, _, err := ComposeSheets(cues, 20, 4, true, nil)
	if err != nil {
		t.Fatalf("ComposeSheets(showRowIndex=true) error = %v", err)
	}
	withoutIndex, _, err := ComposeSheets(cues, 20, 4, false, nil)
	if err != nil {
		t.Fatalf("ComposeSheets(showRowIndex=false) error = %v", err)
	}

	if got, want := withoutIndex[0].Image.Bounds().Dx(), 20; got != want {
		t.Fatalf("width without index = %d, want %d", got, want)
	}
	if got, want := withIndex[0].Image.Bounds().Dx(), 34; got != want {
		t.Fatalf("width with index = %d, want %d", got, want)
	}

	separatorX := 10
	for y := 0; y < withIndex[0].Image.Bounds().Dy(); y++ {
		px := color.RGBAModel.Convert(withIndex[0].Image.At(separatorX, y)).(color.RGBA)
		if px.R != 255 || px.G != 255 || px.B != 255 {
			t.Fatalf("separator pixel at y=%d = %+v, want white", y, px)
		}
	}
}

func TestComposeSheetsProgressCallback(t *testing.T) {
	cues := make([]model.RenderedCue, 0, 5)
	for i := 1; i <= 5; i++ {
		cues = append(cues, cue(i, 8, 3))
	}
	ticks := make([][2]int, 0)
	progress := func(done, total int) {
		ticks = append(ticks, [2]int{done, total})
	}

	sheets, _, err := ComposeSheets(cues, 2, 1, false, progress)
	if err != nil {
		t.Fatalf("ComposeSheets() error = %v", err)
	}
	if len(sheets) != 3 {
		t.Fatalf("len(sheets) = %d, want 3", len(sheets))
	}

	want := [][2]int{{1, 3}, {2, 3}, {3, 3}}
	if len(ticks) != len(want) {
		t.Fatalf("len(ticks) = %d, want %d (%v)", len(ticks), len(want), ticks)
	}
	for i := range want {
		if ticks[i] != want[i] {
			t.Fatalf("ticks[%d] = %v, want %v", i, ticks[i], want[i])
		}
	}
}
