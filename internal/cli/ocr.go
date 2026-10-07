package cli

import (
	"errors"
	"fmt"
	"io"
	"pixelsup-go/internal/ocr"
	"strings"
)

// OCR seams are package variables so command tests can replace external OCR execution.
var runOCROnOutput = ocr.RunOCROnOutput
var runOCROnOutputWithDump = ocr.RunOCROnOutputWithDump

func runOCRCommand(args []string, out io.Writer) error {
	if ocrHelpFlagPresent(args) {
		writeOCRUsage(out)
		return nil
	}

	outputDir := ""
	configPath := "ocr_config.yaml"
	responseDumpDir := ""
	strict := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--config":
			if i+1 >= len(args) {
				return errors.New("failed to parse ocr flags: flag needs an argument: --config")
			}
			configPath = args[i+1]
			i++
		case "--strict":
			strict = true
		case "--dump-paddle-responses":
			if i+1 >= len(args) {
				return errors.New("failed to parse ocr flags: flag needs an argument: --dump-paddle-responses")
			}
			responseDumpDir = args[i+1]
			i++
		case "-h", "--help":
			writeOCRUsage(out)
			return nil
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("failed to parse ocr flags: unknown flag: %s", arg)
			}
			if outputDir != "" {
				return errors.New("ocr requires exactly one output directory")
			}
			outputDir = arg
		}
	}

	if outputDir == "" {
		return errors.New("ocr requires one output directory argument")
	}

	progress := func(done, total int, _ string) {
		progressLine(out, "OCR", done, total)
	}
	var outPath string
	var err error
	if responseDumpDir != "" {
		outPath, err = runOCROnOutputWithDump(outputDir, configPath, strict, responseDumpDir, progress)
	} else {
		outPath, err = runOCROnOutput(outputDir, configPath, strict, progress)
	}
	if err != nil {
		return err
	}
	progressDone(out)
	_, _ = fmt.Fprintf(out, "OCR timeline written to: %s\n", outPath)
	return nil
}

func ocrHelpFlagPresent(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func writeOCRUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup-go ocr <output_dir> [--config <yaml>] [--strict] [--dump-paddle-responses <dir>]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintln(w, "  --config <yaml>  OCR config file path. Default: ./ocr_config.yaml")
	_, _ = fmt.Fprintln(w, "  --strict         Require OCR split line count to match expected count; retry up to 5 times")
	_, _ = fmt.Fprintln(w, "  --dump-paddle-responses <dir>  Save PaddleOCR HTTP responses as per-sheet JSONL files")
	_, _ = fmt.Fprintln(w, "  -h, --help       Show ocr help")
}
