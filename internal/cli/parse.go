package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"pixelsup-go/internal/compose"
	"pixelsup-go/internal/pipeline"
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

// runParserCommand parses flags and delegates the actual work to the pipeline package.
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

	result, err := pipeline.Parse(
		pipeline.ParseOptions{
			Input:      opts.input,
			Output:     opts.output,
			Limit:      opts.limit,
			MaxWidth:   opts.maxWidth,
			Padding:    opts.padding,
			Layout:     opts.layout,
			ForceWhite: opts.forceWhite,
		},
		func(stage string, done, total int) {
			progressLine(out, stage, done, total)
			if done == total {
				progressDone(out)
			}
		},
	)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "Generated %d sheets in %s\n", result.Count, result.OutputDir)
	return nil
}

func parserHelpFlagPresent(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

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
			value, err := strconv.Atoi(args[i+1])
			if err != nil {
				return opts, errors.New("--limit must be an integer")
			}
			opts.limit = value
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
			value, err := strconv.Atoi(args[i+1])
			if err != nil {
				return opts, errors.New("--max-width must be an integer")
			}
			opts.maxWidth = value
			i++
		case "--padding":
			if i+1 >= len(args) {
				return opts, errors.New("failed to parse parse flags: flag needs an argument: --padding")
			}
			value, err := strconv.Atoi(args[i+1])
			if err != nil {
				return opts, errors.New("--padding must be an integer")
			}
			opts.padding = value
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

func writeParserUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup parse <input.sup|input.idx|image_dir> [-o <outdir>] [--limit N] [--layout digits|no-row-index] [--max-width N] [--padding N] [--force-white]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintln(w, "  -o, --output <outdir>  Output directory. Default derived from input path")
	_, _ = fmt.Fprintln(w, "  --limit <n>             Max cues per sheet. Default: 6")
	_, _ = fmt.Fprintln(w, "  --layout <mode>         Sheet layout: digits (default) or no-row-index")
	_, _ = fmt.Fprintln(w, "  --max-width <n>         Max cue width after resize. Default: 1080")
	_, _ = fmt.Fprintln(w, "  --padding <n>           Extra transparent padding around cue. Default: 10")
	_, _ = fmt.Fprintln(w, "  --force-white           Force foreground pixels to white")
}
