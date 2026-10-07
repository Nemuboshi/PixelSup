package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"pixelsup-go/internal/compose"
	"pixelsup-go/internal/model"
	"pixelsup-go/internal/timeline"
	"strconv"
	"strings"
)

type parserOptions struct {
	input      string
	output     string
	limit      int
	maxWidth   int
	padding    int
	layout     compose.SheetLayout
	forceWhite bool
}

// runParserCommand executes subtitle parsing + image preprocessing + sheet composition.
// It supports .sup, .idx(+.sub), or directory input with consecutively numbered PNG/JPG files.
func runParserCommand(args []string, out io.Writer) error {
	if parserHelpFlagPresent(args) {
		writeParserUsage(out)
		return nil
	}

	opts, err := parseParserArgs(args)
	if err != nil {
		return err
	}
	if err := validateParserOptions(opts); err != nil {
		return err
	}

	inputInfo, err := os.Stat(opts.input)
	if err != nil {
		return fmt.Errorf("input path not found: %w", err)
	}

	outputDir := opts.output
	if outputDir == "" {
		if inputInfo.IsDir() {
			outputDir = filepath.Join(filepath.Dir(opts.input), filepath.Base(opts.input)+"_out")
		} else {
			ext := filepath.Ext(opts.input)
			outputDir = strings.TrimSuffix(opts.input, ext)
		}
	}

	progressLine(out, "Loading input", 0, 1)
	rendered, inputKind, err := loadRenderedCues(opts.input, inputInfo)
	if err != nil {
		return err
	}
	progressLine(out, "Loading input", 1, 1)
	progressDone(out)
	if len(rendered) == 0 {
		return errors.New("no subtitle cues decoded from input")
	}

	if err := prepareOutputDir(outputDir); err != nil {
		return err
	}

	processed, err := preprocessCues(
		rendered,
		opts,
		inputKind == "image_dir",
		func(done, total int) { progressLine(out, "Preprocessing", done, total) },
	)
	if err != nil {
		return err
	}
	progressDone(out)

	const defaultDigitSeparatorHeight = 12
	sheets, placements, err := compose.ComposeSheets(
		processed,
		opts.limit,
		defaultDigitSeparatorHeight,
		opts.layout,
		func(done, total int) { progressLine(out, "Composing sheets", done, total) },
	)
	if err != nil {
		return fmt.Errorf("compose output: %w", err)
	}
	progressDone(out)

	for i, sheet := range sheets {
		if err := writePNG(filepath.Join(outputDir, sheet.Name), sheet.Image); err != nil {
			return fmt.Errorf("write %s: %w", sheet.Name, err)
		}
		progressLine(out, "Writing sheets", i+1, len(sheets))
	}
	if len(sheets) > 0 {
		progressDone(out)
	}

	cues := make([]model.SubtitleCue, 0, len(processed))
	for _, rc := range processed {
		cues = append(cues, rc.Cue)
	}

	if err := timeline.WriteSRT(cues, placements, filepath.Join(outputDir, "timeline.srt")); err != nil {
		return fmt.Errorf("write timeline.srt: %w", err)
	}
	if err := timeline.WriteMappingJSON(cues, placements, filepath.Join(outputDir, "mapping.json")); err != nil {
		return fmt.Errorf("write mapping.json: %w", err)
	}

	_, _ = fmt.Fprintf(out, "Generated %d sheets in %s\n", len(sheets), outputDir)
	return nil
}

// runExportCommand writes one cue image per subtitle event and emits timeline files

func parserHelpFlagPresent(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// parseParserArgs validates parse flags and returns parsed options.
func parseParserArgs(args []string) (parserOptions, error) {
	opts := parserOptions{limit: 6, maxWidth: 1080, padding: 10, layout: compose.LayoutDigits}
	inputs := make([]string, 0, 1)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-o", "--output":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("failed to parse parse flags: flag needs an argument: %s", arg)
			}
			opts.output = args[i+1]
			i++
		case "--limit":
			if i+1 >= len(args) {
				return opts, errors.New("failed to parse parse flags: flag needs an argument: --limit")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return opts, errors.New("--limit must be an integer")
			}
			opts.limit = v
			i++
		case "--layout":
			if i+1 >= len(args) {
				return opts, errors.New("failed to parse parse flags: flag needs an argument: --layout")
			}
			opts.layout = compose.SheetLayout(args[i+1])
			i++
		case "--max-width":
			if i+1 >= len(args) {
				return opts, errors.New("failed to parse parse flags: flag needs an argument: --max-width")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return opts, errors.New("--max-width must be an integer")
			}
			opts.maxWidth = v
			i++
		case "--padding":
			if i+1 >= len(args) {
				return opts, errors.New("failed to parse parse flags: flag needs an argument: --padding")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return opts, errors.New("--padding must be an integer")
			}
			opts.padding = v
			i++
		case "--force-white":
			opts.forceWhite = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("failed to parse parse flags: unknown flag: %s", arg)
			}
			inputs = append(inputs, arg)
		}
	}

	if len(inputs) != 1 {
		return opts, errors.New("parse requires exactly one input path")
	}
	opts.input = inputs[0]
	return opts, nil
}

// parseExportArgs validates export flags for a single binary subtitle input file.

func validateParserOptions(opts parserOptions) error {
	if opts.limit <= 0 {
		return errors.New("--limit must be > 0")
	}
	if opts.maxWidth <= 0 {
		return errors.New("--max-width must be > 0")
	}
	if opts.padding < 0 {
		return errors.New("--padding must be >= 0")
	}
	if opts.layout != compose.LayoutDigits && opts.layout != compose.LayoutNoRowIndex {
		return fmt.Errorf("--layout must be one of: %s, %s", compose.LayoutDigits, compose.LayoutNoRowIndex)
	}
	return nil
}

// writeParserUsage documents parse invocation and supported flags.
func writeParserUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup-go parse <input.sup|input.idx|image_dir> [-o <outdir>] [--limit N] [--layout digits|no-row-index] [--max-width N] [--padding N] [--force-white]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintln(w, "  -o, --output <outdir>  Output directory. Default derived from input path")
	_, _ = fmt.Fprintln(w, "  --limit <n>             Max cues per sheet. Default: 6")
	_, _ = fmt.Fprintln(w, "  --layout <mode>         Sheet layout: digits (default) or no-row-index")
	_, _ = fmt.Fprintln(w, "  --max-width <n>         Max cue width after resize. Default: 1080")
	_, _ = fmt.Fprintln(w, "  --padding <n>           Extra transparent padding around cue. Default: 10")
	_, _ = fmt.Fprintln(w, "  --force-white           Force foreground pixels to white")
}
