package cli

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"pixelsup-go/internal/compose"
	"pixelsup-go/internal/imageops"
	"pixelsup-go/internal/model"
	"pixelsup-go/internal/ocr"
	"pixelsup-go/internal/parser/idxsub"
	"pixelsup-go/internal/parser/sup"
	"pixelsup-go/internal/timeline"
)

// runOCROnOutput is a test seam that defaults to the production OCR runner.
var runOCROnOutput = ocr.RunOCROnOutput
var progressLineActive bool

// Command is a lightweight command descriptor used in the initial migration phase.
// It keeps subcommand definitions explicit without introducing external dependencies.
type Command struct {
	name        string
	description string
	run         func(args []string, out io.Writer) error
}

// Name returns the command name used on CLI.
func (c *Command) Name() string {
	return c.name
}

// RootCommand manages top-level dispatch for parse/export/ocr subcommands.
// This structure mirrors the future command tree and can be replaced by Cobra later if desired.
type RootCommand struct {
	commands map[string]*Command
}

// BuildRootCommand constructs the root CLI command with currently supported subcommands.
func BuildRootCommand() *RootCommand {
	parseCmd := &Command{
		name:        "parse",
		description: "Parse .sup, .idx(+.sub), or numbered image dirs into sheets + timeline outputs.",
		run:         runParserCommand,
	}
	exportCmd := &Command{
		name:        "export",
		description: "Export .sup/.idx cues as per-cue PNG files with timeline outputs.",
		run:         runExportCommand,
	}
	ocrCmd := &Command{
		name:        "ocr",
		description: "Run OCR on composed sheets and backfill subtitle text.",
		run:         runOCRCommand,
	}

	return &RootCommand{
		commands: map[string]*Command{
			parseCmd.name:  parseCmd,
			exportCmd.name: exportCmd,
			ocrCmd.name:    ocrCmd,
		},
	}
}

// Commands returns all direct child subcommands.
func (r *RootCommand) Commands() []*Command {
	out := make([]*Command, 0, len(r.commands))
	if parseCmd, ok := r.commands["parse"]; ok {
		out = append(out, parseCmd)
	}
	if exportCmd, ok := r.commands["export"]; ok {
		out = append(out, exportCmd)
	}
	if ocrCmd, ok := r.commands["ocr"]; ok {
		out = append(out, ocrCmd)
	}
	return out
}

// Execute dispatches command-line arguments to a matching subcommand.
func (r *RootCommand) Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stdout, r)
		return 0
	}

	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		writeUsage(stdout, r)
		return 0
	}

	cmd, ok := r.commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		writeUsage(stderr, r)
		return 2
	}

	if err := cmd.run(args[1:], stdout); err != nil {
		if progressLineActive {
			_, _ = fmt.Fprintln(stderr)
			progressLineActive = false
		}
		_, _ = fmt.Fprintf(stderr, "command %q failed: %v\n", cmd.name, err)
		return 1
	}
	return 0
}

func writeUsage(w io.Writer, r *RootCommand) {
	_, _ = fmt.Fprintln(w, "pixelsup-go: subtitle tooling")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup-go <command> [args]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Commands:")
	for _, cmd := range r.Commands() {
		_, _ = fmt.Fprintf(w, "  %-8s %s\n", cmd.name, cmd.description)
	}
}

type parserOptions struct {
	input      string
	output     string
	limit      int
	maxWidth   int
	padding    int
	forceWhite bool
}

type exportOptions struct {
	input  string
	output string
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
	sheets, placements, err := compose.ComposeSheetsWithDigitSeparator(
		processed,
		opts.limit,
		defaultDigitSeparatorHeight,
		func(done, total int) { progressLine(out, "Composing digits", done, total) },
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

func runOCRCommand(args []string, out io.Writer) error {
	if ocrHelpFlagPresent(args) {
		writeOCRUsage(out)
		return nil
	}

	outputDir := ""
	configPath := "ocr_config.yaml"
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

	outPath, err := runOCROnOutput(outputDir, configPath, strict, func(done, total int, _ string) {
		progressLine(out, "OCR", done, total)
	})
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

func exportHelpFlagPresent(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func writeOCRUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup-go ocr <output_dir> [--config <yaml>]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintln(w, "  --config <yaml>  OCR config file path. Default: ./ocr_config.yaml")
	_, _ = fmt.Fprintln(w, "  --strict         Require OCR split line count to match expected count; retry up to 5 times")
	_, _ = fmt.Fprintln(w, "  -h, --help       Show ocr help")
}

func writeExportUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup-go export <input.sup|input.idx> [-o <outdir>]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintln(w, "  -o, --output <outdir>  Output directory. Default derived from input path")
	_, _ = fmt.Fprintln(w, "  -h, --help             Show export help")
}

func loadRenderedCues(input string, info os.FileInfo) ([]model.RenderedCue, string, error) {
	if info.IsDir() {
		cues, err := loadCuesFromImageDir(input)
		if err != nil {
			return nil, "", err
		}
		return cues, "image_dir", nil
	}

	ext := strings.ToLower(filepath.Ext(input))
	raw, err := os.ReadFile(input)
	if err != nil {
		return nil, "", fmt.Errorf("read input file: %w", err)
	}

	switch ext {
	case ".sup":
		rendered, err := sup.ParseCuesAndFrames(raw)
		if err != nil {
			return nil, "", fmt.Errorf("parse SUP payload: %w", err)
		}
		return rendered, "sup", nil
	case ".idx":
		subPath := strings.TrimSuffix(input, filepath.Ext(input)) + ".sub"
		subData, err := os.ReadFile(subPath)
		if err != nil {
			return nil, "", fmt.Errorf("read matching .sub file: %w", err)
		}
		rendered, err := idxsub.ParseCuesAndFrames(string(raw), subData)
		if err != nil {
			return nil, "", fmt.Errorf("parse IDX/SUB payload: %w", err)
		}
		return rendered, "idx", nil
	default:
		return nil, "", errors.New("input file must be .sup or .idx, or provide a directory of numbered images")
	}
}

func preprocessCues(
	cues []model.RenderedCue,
	opts parserOptions,
	solidBgFallback bool,
	progress func(done, total int),
) ([]model.RenderedCue, error) {
	processed := make([]model.RenderedCue, 0, len(cues))
	total := len(cues)
	for i, cue := range cues {
		img := image.Image(cue.Frame)
		cropped := imageops.AutocropNonTransparent(img, solidBgFallback)
		padded, err := imageops.AddInnerPadding(cropped, opts.padding)
		if err != nil {
			return nil, err
		}
		resized, err := imageops.ResizeToMaxWidth(padded, opts.maxWidth)
		if err != nil {
			return nil, err
		}
		if opts.forceWhite {
			resized = imageops.ForceWhiteForeground(resized)
		}

		rgba := toRGBA(resized)
		processed = append(processed, model.RenderedCue{Cue: cue.Cue, Frame: rgba})
		if progress != nil {
			progress(i+1, total)
		}
	}
	return processed, nil
}

func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func prepareOutputDir(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	matches, _ := filepath.Glob(filepath.Join(outputDir, "sheet_*.png"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
	_ = os.Remove(filepath.Join(outputDir, "timeline.srt"))
	_ = os.Remove(filepath.Join(outputDir, "mapping.json"))
	_ = os.RemoveAll(filepath.Join(outputDir, "temp"))
	return nil
}

// prepareExportOutputDir clears stale export artifacts while preserving unrelated files.
func prepareExportOutputDir(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	cueMatches, _ := filepath.Glob(filepath.Join(outputDir, "cue_*.png"))
	for _, m := range cueMatches {
		_ = os.Remove(m)
	}
	_ = os.Remove(filepath.Join(outputDir, "timeline.srt"))
	_ = os.Remove(filepath.Join(outputDir, "mapping.json"))
	return nil
}

func loadCuesFromImageDir(inputDir string) ([]model.RenderedCue, error) {
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		return nil, err
	}
	type pair struct {
		n int
		p string
	}
	numbered := make([]pair, 0)
	suffix := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
			continue
		}
		if suffix == "" {
			suffix = ext
		} else if suffix != ext {
			return nil, errors.New("image directory must use a single format only (all .png or all .jpg)")
		}
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		n, err := strconv.Atoi(stem)
		if err != nil {
			return nil, fmt.Errorf("image filename must be numeric: %s", e.Name())
		}
		numbered = append(numbered, pair{n: n, p: filepath.Join(inputDir, e.Name())})
	}
	if len(numbered) == 0 {
		return nil, errors.New("no .png/.jpg files found in directory")
	}
	sort.Slice(numbered, func(i, j int) bool { return numbered[i].n < numbered[j].n })
	for i := 1; i < len(numbered); i++ {
		if numbered[i].n == numbered[i-1].n {
			return nil, fmt.Errorf("duplicate image number: %d", numbered[i].n)
		}
	}
	expected := numbered[0].n
	for _, item := range numbered {
		if item.n != expected {
			return nil, fmt.Errorf("image numbers must be consecutive; missing number: %d", expected)
		}
		expected++
	}

	cues := make([]model.RenderedCue, 0, len(numbered))
	for i, item := range numbered {
		f, err := os.Open(item.p)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("decode image %s: %w", item.p, err)
		}
		rgba := toRGBA(img)
		cues = append(cues, model.RenderedCue{Cue: model.SubtitleCue{Index: item.n, StartMS: i * 1000, EndMS: (i + 1) * 1000}, Frame: rgba})
	}
	return cues, nil
}

// parserHelpFlagPresent is checked before parsing so help intent is honored
// even when required flags are omitted.
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
	opts := parserOptions{limit: 6, maxWidth: 1080, padding: 10}
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
	return nil
}

// writeParserUsage documents parse invocation and supported flags.
func writeParserUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup-go parse <input.sup|input.idx|image_dir> [-o <outdir>] [--limit N] [--max-width N] [--padding N] [--force-white]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Options:")
	_, _ = fmt.Fprintln(w, "  -o, --output <outdir>  Output directory. Default derived from input path")
	_, _ = fmt.Fprintln(w, "  --limit <n>             Max cues per sheet. Default: 6")
	_, _ = fmt.Fprintln(w, "  --max-width <n>         Max cue width after resize. Default: 1080")
	_, _ = fmt.Fprintln(w, "  --padding <n>           Extra transparent padding around cue. Default: 10")
	_, _ = fmt.Fprintln(w, "  --force-white           Force foreground pixels to white")
}

func progressLine(w io.Writer, label string, done, total int) {
	if w == nil {
		return
	}
	progressLineActive = true
	if total <= 0 {
		total = 1
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	const width = 24
	filled := done * width / total
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	pct := done * 100 / total
	_, _ = fmt.Fprintf(w, "\r%-16s [%s] %3d%% (%d/%d)", label, bar, pct, done, total)
}

func progressDone(w io.Writer) {
	if w == nil {
		return
	}
	progressLineActive = false
	_, _ = fmt.Fprintln(w)
}
