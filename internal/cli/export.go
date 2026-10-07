package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"pixelsup-go/internal/pipeline"
)

type exportOptions struct {
	input  string
	output string
}

// runExportCommand parses flags and delegates per-cue export to the pipeline package.
func runExportCommand(args []string, out io.Writer) error {
	if exportHelpFlagPresent(args) {
		writeExportUsage(out)
		return nil
	}

	opts, err := parseExportArgs(args)
	if err != nil {
		return err
	}

	result, err := pipeline.Export(
		pipeline.ExportOptions{Input: opts.input, Output: opts.output},
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

	_, _ = fmt.Fprintf(out, "Exported %d cue images in %s\n", result.Count, result.OutputDir)
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
