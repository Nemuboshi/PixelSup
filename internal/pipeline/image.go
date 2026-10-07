package pipeline

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"pixelsup-go/internal/imageops"
	"pixelsup-go/internal/model"
)

func preprocessCues(
	cues []model.RenderedCue,
	opts ParseOptions,
	solidBgFallback bool,
	progress func(done, total int),
) ([]model.RenderedCue, error) {
	processed := make([]model.RenderedCue, 0, len(cues))
	total := len(cues)
	for i, cue := range cues {
		img := image.Image(cue.Frame)
		cropped := imageops.AutocropNonTransparent(img, solidBgFallback)
		padded, err := imageops.AddInnerPadding(cropped, opts.Padding)
		if err != nil {
			return nil, err
		}
		resized, err := imageops.ResizeToMaxWidth(padded, opts.MaxWidth)
		if err != nil {
			return nil, err
		}
		if opts.ForceWhite {
			resized = imageops.ForceWhiteForeground(resized)
		}

		processed = append(processed, model.RenderedCue{Cue: cue.Cue, Frame: toRGBA(resized)})
		if progress != nil {
			progress(i+1, total)
		}
	}
	return processed, nil
}

func toRGBA(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
	return dst
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, img)
}

func prepareOutputDir(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	matches, _ := filepath.Glob(filepath.Join(outputDir, "sheet_*.png"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	_ = os.Remove(filepath.Join(outputDir, "timeline.srt"))
	_ = os.Remove(filepath.Join(outputDir, "mapping.json"))
	_ = os.RemoveAll(filepath.Join(outputDir, "temp"))
	return nil
}

func prepareExportOutputDir(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	matches, _ := filepath.Glob(filepath.Join(outputDir, "cue_*.png"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	_ = os.Remove(filepath.Join(outputDir, "timeline.srt"))
	_ = os.Remove(filepath.Join(outputDir, "mapping.json"))
	return nil
}
