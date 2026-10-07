package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pixelsup-go/internal/model"
	"pixelsup-go/internal/timeline"
)

// Export writes one image per subtitle cue and emits matching timeline files.
func Export(opts ExportOptions, progress ProgressFunc) (Result, error) {
	inputInfo, err := os.Stat(opts.Input)
	if err != nil {
		return Result{}, fmt.Errorf("input path not found: %w", err)
	}
	if inputInfo.IsDir() {
		return Result{}, errors.New("export input must be .sup or .idx")
	}

	outputDir := opts.Output
	if outputDir == "" {
		ext := filepath.Ext(opts.Input)
		outputDir = strings.TrimSuffix(opts.Input, ext)
	}
	if err := prepareExportOutputDir(outputDir); err != nil {
		return Result{}, err
	}

	report(progress, "Loading input", 0, 1)
	rendered, inputKind, err := loadRenderedCues(opts.Input, inputInfo)
	if err != nil {
		return Result{}, err
	}
	if inputKind != "sup" && inputKind != "idx" {
		return Result{}, errors.New("export input must be .sup or .idx")
	}
	report(progress, "Loading input", 1, 1)
	if len(rendered) == 0 {
		return Result{}, errors.New("no subtitle cues decoded from input")
	}

	placements := make(map[int]model.CuePlacement, len(rendered))
	cues := make([]model.SubtitleCue, 0, len(rendered))
	for i, renderedCue := range rendered {
		imageName := fmt.Sprintf("cue_%05d.png", renderedCue.Cue.Index)
		if err := writePNG(filepath.Join(outputDir, imageName), renderedCue.Frame); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", imageName, err)
		}
		placements[renderedCue.Cue.Index] = model.CuePlacement{
			SheetName:       imageName,
			PositionInSheet: 1,
		}
		cues = append(cues, renderedCue.Cue)
		report(progress, "Writing cues", i+1, len(rendered))
	}

	if err := timeline.WriteSRT(cues, placements, filepath.Join(outputDir, "timeline.srt")); err != nil {
		return Result{}, fmt.Errorf("write timeline.srt: %w", err)
	}
	if err := timeline.WriteMappingJSON(cues, placements, filepath.Join(outputDir, "mapping.json")); err != nil {
		return Result{}, fmt.Errorf("write mapping.json: %w", err)
	}

	return Result{OutputDir: outputDir, Count: len(cues)}, nil
}
