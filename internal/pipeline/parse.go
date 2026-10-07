package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pixelsup-go/internal/compose"
	"pixelsup-go/internal/model"
	"pixelsup-go/internal/timeline"
)

const defaultDigitSeparatorHeight = 12

// Parse converts a supported subtitle source or numbered image directory into sheets and timeline files.
func Parse(opts ParseOptions, progress ProgressFunc) (Result, error) {
	inputInfo, err := os.Stat(opts.Input)
	if err != nil {
		return Result{}, fmt.Errorf("input path not found: %w", err)
	}

	outputDir := opts.Output
	if outputDir == "" {
		if inputInfo.IsDir() {
			outputDir = filepath.Join(filepath.Dir(opts.Input), filepath.Base(opts.Input)+"_out")
		} else {
			ext := filepath.Ext(opts.Input)
			outputDir = strings.TrimSuffix(opts.Input, ext)
		}
	}

	report(progress, "Loading input", 0, 1)
	rendered, inputKind, err := loadRenderedCues(opts.Input, inputInfo)
	if err != nil {
		return Result{}, err
	}
	report(progress, "Loading input", 1, 1)
	if len(rendered) == 0 {
		return Result{}, errors.New("no subtitle cues decoded from input")
	}

	if err := prepareOutputDir(outputDir); err != nil {
		return Result{}, err
	}

	processed, err := preprocessCues(
		rendered,
		opts,
		inputKind == "image_dir",
		func(done, total int) { report(progress, "Preprocessing", done, total) },
	)
	if err != nil {
		return Result{}, err
	}

	sheets, placements, err := compose.ComposeSheets(
		processed,
		opts.Limit,
		defaultDigitSeparatorHeight,
		opts.Layout,
		func(done, total int) { report(progress, "Composing sheets", done, total) },
	)
	if err != nil {
		return Result{}, fmt.Errorf("compose output: %w", err)
	}

	for i, sheet := range sheets {
		if err := writePNG(filepath.Join(outputDir, sheet.Name), sheet.Image); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", sheet.Name, err)
		}
		report(progress, "Writing sheets", i+1, len(sheets))
	}

	cues := make([]model.SubtitleCue, 0, len(processed))
	for _, rc := range processed {
		cues = append(cues, rc.Cue)
	}

	if err := timeline.WriteSRT(cues, placements, filepath.Join(outputDir, "timeline.srt")); err != nil {
		return Result{}, fmt.Errorf("write timeline.srt: %w", err)
	}
	if err := timeline.WriteMappingJSON(cues, placements, filepath.Join(outputDir, "mapping.json")); err != nil {
		return Result{}, fmt.Errorf("write mapping.json: %w", err)
	}

	return Result{OutputDir: outputDir, Count: len(sheets)}, nil
}
