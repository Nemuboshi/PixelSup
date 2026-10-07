package pipeline

import "pixelsup-go/internal/compose"

// ProgressFunc reports pipeline stage progress.
type ProgressFunc func(stage string, done, total int)

// ParseOptions controls subtitle parsing, preprocessing, and sheet composition.
type ParseOptions struct {
	Input      string
	Output     string
	Limit      int
	MaxWidth   int
	Padding    int
	Layout     compose.SheetLayout
	ForceWhite bool
}

// ExportOptions controls per-cue image export.
type ExportOptions struct {
	Input  string
	Output string
}

// Result describes files written by a pipeline command.
type Result struct {
	OutputDir string
	Count     int
}

func report(progress ProgressFunc, stage string, done, total int) {
	if progress != nil {
		progress(stage, done, total)
	}
}
