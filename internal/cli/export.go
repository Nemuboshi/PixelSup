package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"pixelsup-go/internal/model"
	"pixelsup-go/internal/timeline"
	"strings"
)

type exportOptions struct {
	input  string
	output string
}

// that point directly to those cue images instead of composed sheet coordinates.
//
// This path intentionally keeps source timings untouched by bypassing composition.
func runExportCommand(args []string, out io.Writer) error {
	if exportHelpFlagPresent(args) {
		writeExportUsage(out)
		return nil
	}

	opts, err := parseExportArgs(args)
	if err != nil {
		return err
	}

	inputInfo, err := os.Stat(opts.input)
	if err != nil {
		return fmt.Errorf("input path not found: %w", err)
	}
	if inputInfo.IsDir() {
		return errors.New("export input must be .sup or .idx")
	}

	outputDir := opts.output
	if outputDir == "" {
		ext := filepath.Ext(opts.input)
		outputDir = strings.TrimSuffix(opts.input, ext)
	}
	if err := prepareExportOutputDir(outputDir); err != nil {
		return err
	}

	progressLine(out, "Loading input", 0, 1)
	rendered, inputKind, err := loadRenderedCues(opts.input, inputInfo)
	if err != nil {
		return err
	}
	if inputKind != "sup" && inputKind != "idx" {
		return errors.New("export input must be .sup or .idx")
	}
	progressLine(out, "Loading input", 1, 1)
	progressDone(out)
	if len(rendered) == 0 {
		return errors.New("no subtitle cues decoded from input")
	}

	placements := make(map[int]model.CuePlacement, len(rendered))
	cues := make([]model.SubtitleCue, 0, len(rendered))

	// Export keeps original decoded frames to preserve cue image fidelity and timing.
	for i, renderedCue := range rendered {
		imageName := fmt.Sprintf("cue_%05d.png", renderedCue.Cue.Index)
		if err := writePNG(filepath.Join(outputDir, imageName), renderedCue.Frame); err != nil {
			return fmt.Errorf("write %s: %w", imageName, err)
		}
		placements[renderedCue.Cue.Index] = model.CuePlacement{
			SheetName:       imageName,
			PositionInSheet: 1,
		}
		cues = append(cues, renderedCue.Cue)
		progressLine(out, "Writing cues", i+1, len(rendered))
	}
	progressDone(out)

	if err := timeline.WriteSRT(cues, placements, filepath.Join(outputDir, "timeline.srt")); err != nil {
		return fmt.Errorf("write timeline.srt: %w", err)
	}
	if err := timeline.WriteMappingJSON(cues, placements, filepath.Join(outputDir, "mapping.json")); err != nil {
		return fmt.Errorf("write mapping.json: %w", err)
	}

	_, _ = fmt.Fprintf(out, "Exported %d cue images in %s\n", len(cues), outputDir)
	return nil
}

func exportHelpFlagPresent(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func writeExportUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup-go export <input.sup|input.idx> [-o <outdir>]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintln(w, "  -o, --output <outdir>  Output directory. Default derived from input path")
	_, _ = fmt.Fprintln(w, "  -h, --help             Show export help")
}

func parseExportArgs(args []string) (exportOptions, error) {
	opts := exportOptions{}
	inputs := make([]string, 0, 1)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-o", "--output":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("failed to parse export flags: flag needs an argument: %s", arg)
			}
			opts.output = args[i+1]
			i++
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("failed to parse export flags: unknown flag: %s", arg)
			}
			inputs = append(inputs, arg)
		}
	}

	if len(inputs) != 1 {
		return opts, errors.New("export requires exactly one input path")
	}
	opts.input = inputs[0]
	return opts, nil
}
