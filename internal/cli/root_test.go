package cli

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pixelsup-go/internal/ocr"
)

func TestBuildRootCommand_HasExpectedSubcommands(t *testing.T) {
	cmd := BuildRootCommand()
	if cmd == nil {
		t.Fatalf("BuildRootCommand() returned nil")
	}
	got := map[string]bool{}
	for _, c := range cmd.Commands() {
		got[c.Name()] = true
	}
	if !got["parse"] {
		t.Fatalf("missing parse subcommand")
	}
	if !got["export"] {
		t.Fatalf("missing export subcommand")
	}
	if !got["ocr"] {
		t.Fatalf("missing ocr subcommand")
	}
}

func TestExecute_Help(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer
	code := root.Execute([]string{"--help"}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0 for help, got %d", code)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Fatalf("expected help output to contain command list, got: %q", out.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr for help, got: %q", err.String())
	}
}

func TestExecute_UnknownCommand(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer
	code := root.Execute([]string{"unknown-cmd"}, &out, &err)
	if code != 2 {
		t.Fatalf("expected exit code 2 for unknown command, got %d", code)
	}
	if !strings.Contains(err.String(), "unknown command") {
		t.Fatalf("expected stderr to include unknown command message, got: %q", err.String())
	}
}

func TestExecute_Parse_Help(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer
	code := root.Execute([]string{"parse", "--help"}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0 for parse help, got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected parse help output, got: %q", out.String())
	}
	if strings.Contains(out.String(), "--keep-temp") {
		t.Fatalf("parse help should not include removed --keep-temp option, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "Max cues per sheet. Default: 6") {
		t.Fatalf("parse help should advertise tightened default limit, got: %q", out.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr for parse help, got: %q", err.String())
	}
}

func TestParseParserArgs_DefaultLimitIsSix(t *testing.T) {
	opts, err := parseParserArgs([]string{"input.sup"})
	if err != nil {
		t.Fatalf("parseParserArgs: %v", err)
	}
	if opts.limit != 6 {
		t.Fatalf("default limit = %d, want 6", opts.limit)
	}
}

func TestExecute_ParseValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		errMatch string
	}{
		{name: "missing input", args: []string{"parse", "--limit", "1"}, errMatch: "parse requires exactly one input path"},
		{name: "unknown flag", args: []string{"parse", "input.sup", "--bad"}, errMatch: "failed to parse parse flags"},
		{name: "removed keep-temp flag", args: []string{"parse", "input.sup", "--keep-temp"}, errMatch: "failed to parse parse flags: unknown flag: --keep-temp"},
		{name: "removed mode flag", args: []string{"parse", "input.sup", "--mode", "digits"}, errMatch: "failed to parse parse flags: unknown flag: --mode"},
		{name: "removed gap flag", args: []string{"parse", "input.sup", "--gap", "12"}, errMatch: "failed to parse parse flags: unknown flag: --gap"},
		{name: "removed no-row-index flag", args: []string{"parse", "input.sup", "--no-row-index"}, errMatch: "failed to parse parse flags: unknown flag: --no-row-index"},
	}

	root := BuildRootCommand()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			var err bytes.Buffer
			code := root.Execute(tt.args, &out, &err)
			if code != 1 {
				t.Fatalf("expected exit code 1, got %d (stderr=%q)", code, err.String())
			}
			if !strings.Contains(err.String(), tt.errMatch) {
				t.Fatalf("expected stderr to include %q, got %q", tt.errMatch, err.String())
			}
		})
	}
}

func TestExecute_Export_Help(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer
	code := root.Execute([]string{"export", "--help"}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0 for export help, got %d", code)
	}
	if !strings.Contains(out.String(), "pixelsup-go export") {
		t.Fatalf("expected export help output, got: %q", out.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr for export help, got: %q", err.String())
	}
}

func TestExecute_Export_ArgumentErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		errMatch string
	}{
		{name: "missing input", args: []string{"export"}, errMatch: "export requires exactly one input path"},
		{name: "unknown flag", args: []string{"export", "input.sup", "--bad"}, errMatch: "failed to parse export flags: unknown flag: --bad"},
	}

	root := BuildRootCommand()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			var err bytes.Buffer
			code := root.Execute(tt.args, &out, &err)
			if code != 1 {
				t.Fatalf("expected exit code 1, got %d (stderr=%q)", code, err.String())
			}
			if !strings.Contains(err.String(), tt.errMatch) {
				t.Fatalf("expected stderr to include %q, got %q", tt.errMatch, err.String())
			}
		})
	}
}

func TestExecute_ExportValidation_RejectsUnsupportedInputs(t *testing.T) {
	root := BuildRootCommand()

	baseDir := t.TempDir()
	inputDir := filepath.Join(baseDir, "images")
	if mkErr := os.MkdirAll(inputDir, 0o755); mkErr != nil {
		t.Fatalf("failed to create input dir fixture: %v", mkErr)
	}
	badInput := filepath.Join(baseDir, "input.bad")
	if writeErr := os.WriteFile(badInput, []byte("x"), 0o644); writeErr != nil {
		t.Fatalf("failed to create invalid input fixture: %v", writeErr)
	}

	tests := []struct {
		name     string
		args     []string
		errMatch string
	}{
		{name: "directory input", args: []string{"export", inputDir}, errMatch: "export input must be .sup or .idx"},
		{name: "invalid extension", args: []string{"export", badInput}, errMatch: "input file must be .sup or .idx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			var err bytes.Buffer
			code := root.Execute(tt.args, &out, &err)
			if code != 1 {
				t.Fatalf("expected exit code 1, got %d (stderr=%q)", code, err.String())
			}
			if !strings.Contains(err.String(), tt.errMatch) {
				t.Fatalf("expected stderr to include %q, got %q", tt.errMatch, err.String())
			}
		})
	}
}

func TestExecute_ExportSuccess_WritesCueImagesAndTimeline(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer

	baseDir := t.TempDir()
	inputPath := filepath.Join(baseDir, "sample.sup")
	outputDir := filepath.Join(baseDir, "exported")

	if writeErr := os.WriteFile(inputPath, sampleSUPPayload(), 0o644); writeErr != nil {
		t.Fatalf("failed to prepare input sup fixture: %v", writeErr)
	}

	code := root.Execute([]string{"export", inputPath, "-o", outputDir}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, err.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", err.String())
	}

	for _, filename := range []string{"cue_00001.png", "cue_00002.png", "timeline.srt", "mapping.json"} {
		if _, statErr := os.Stat(filepath.Join(outputDir, filename)); statErr != nil {
			t.Fatalf("%s was not generated: %v", filename, statErr)
		}
	}

	srtPath := filepath.Join(outputDir, "timeline.srt")
	srtBytes, readErr := os.ReadFile(srtPath)
	if readErr != nil {
		t.Fatalf("failed to read timeline.srt: %v", readErr)
	}
	srt := string(srtBytes)
	if !strings.Contains(srt, "00:00:01,000 --> 00:00:02,000") {
		t.Fatalf("expected source cue timing to be preserved, got timeline.srt=%q", srt)
	}
	if !strings.Contains(srt, "[img:cue_00001.png#01]") {
		t.Fatalf("expected timeline.srt to point at exported cue image, got %q", srt)
	}

	if !strings.Contains(out.String(), "Exported 2 cue images") {
		t.Fatalf("expected export summary output, got: %q", out.String())
	}
}

func TestExecute_ParseValidation_InvalidExtension(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer

	baseDir := t.TempDir()
	badInput := filepath.Join(baseDir, "input.bad")
	if writeErr := os.WriteFile(badInput, []byte("x"), 0o644); writeErr != nil {
		t.Fatalf("failed to create invalid input fixture: %v", writeErr)
	}

	code := root.Execute([]string{"parse", badInput}, &out, &err)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (stderr=%q)", code, err.String())
	}
	if !strings.Contains(err.String(), "input file must be .sup or .idx") {
		t.Fatalf("expected invalid extension message, got %q", err.String())
	}
}

func TestExecute_ParseSuccess_WritesArtifacts(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer

	baseDir := t.TempDir()
	inputPath := filepath.Join(baseDir, "sample.sup")
	outputDir := filepath.Join(baseDir, "out")

	if writeErr := os.WriteFile(inputPath, sampleSUPPayload(), 0o644); writeErr != nil {
		t.Fatalf("failed to prepare input sup fixture: %v", writeErr)
	}

	code := root.Execute([]string{"parse", inputPath, "-o", outputDir}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, err.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", err.String())
	}

	for _, filename := range []string{"timeline.srt", "mapping.json", "sheet_0001.png"} {
		if _, statErr := os.Stat(filepath.Join(outputDir, filename)); statErr != nil {
			t.Fatalf("%s was not generated: %v", filename, statErr)
		}
	}
	if !strings.Contains(out.String(), "Generated") {
		t.Fatalf("expected generation summary output, got: %q", out.String())
	}
}

func TestExecute_OCR_Help(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer
	code := root.Execute([]string{"ocr", "--help"}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0 for ocr help, got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected ocr help output, got: %q", out.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr for ocr help, got: %q", err.String())
	}
}

func TestExecute_ParseSuccess_DefaultOutputFromFile(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer

	baseDir := t.TempDir()
	inputPath := filepath.Join(baseDir, "movie.sup")
	if writeErr := os.WriteFile(inputPath, sampleSUPPayload(), 0o644); writeErr != nil {
		t.Fatalf("failed to prepare input sup fixture: %v", writeErr)
	}

	code := root.Execute([]string{"parse", inputPath}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, err.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", err.String())
	}

	defaultOutputDir := strings.TrimSuffix(inputPath, ".sup")
	for _, filename := range []string{"timeline.srt", "mapping.json", "sheet_0001.png"} {
		if _, statErr := os.Stat(filepath.Join(defaultOutputDir, filename)); statErr != nil {
			t.Fatalf("%s was not generated in default output dir: %v", filename, statErr)
		}
	}
}

func TestExecute_ParseSuccess_ImageDirInput_DefaultOutput(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer

	baseDir := t.TempDir()
	imageDir := filepath.Join(baseDir, "frames")
	if mkErr := os.MkdirAll(imageDir, 0o755); mkErr != nil {
		t.Fatalf("failed to create image dir: %v", mkErr)
	}

	for i := 1; i <= 2; i++ {
		imgPath := filepath.Join(imageDir, strconv.Itoa(i)+".png")
		if writeErr := writeSolidPNG(imgPath, 24, 12); writeErr != nil {
			t.Fatalf("failed to write image fixture %s: %v", imgPath, writeErr)
		}
	}

	code := root.Execute([]string{"parse", imageDir}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, err.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", err.String())
	}

	defaultOutputDir := filepath.Join(baseDir, "frames_out")
	for _, filename := range []string{"timeline.srt", "mapping.json", "sheet_0001.png"} {
		if _, statErr := os.Stat(filepath.Join(defaultOutputDir, filename)); statErr != nil {
			t.Fatalf("%s was not generated from image dir input: %v", filename, statErr)
		}
	}
}

func TestExecute_ParseValidation_NumericRangeErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		errMatch string
	}{
		{name: "limit must be positive", args: []string{"parse", "input.sup", "--limit", "0"}, errMatch: "--limit must be > 0"},
		{name: "max-width must be positive", args: []string{"parse", "input.sup", "--max-width", "0"}, errMatch: "--max-width must be > 0"},
		{name: "padding cannot be negative", args: []string{"parse", "input.sup", "--padding", "-1"}, errMatch: "--padding must be >= 0"},
	}

	root := BuildRootCommand()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			var err bytes.Buffer
			code := root.Execute(tt.args, &out, &err)
			if code != 1 {
				t.Fatalf("expected exit code 1, got %d (stderr=%q)", code, err.String())
			}
			if !strings.Contains(err.String(), tt.errMatch) {
				t.Fatalf("expected stderr to include %q, got %q", tt.errMatch, err.String())
			}
		})
	}
}

func TestExecute_ParseErrorAfterProgressStartsOnNewLine(t *testing.T) {
	root := BuildRootCommand()
	var combined bytes.Buffer

	baseDir := t.TempDir()
	inputDir := filepath.Join(baseDir, "images")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatalf("failed to create image dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "sheet_0001.png"), []byte("not-an-image"), 0o644); err != nil {
		t.Fatalf("failed to create image fixture: %v", err)
	}

	code := root.Execute([]string{"parse", inputDir}, &combined, &combined)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (output=%q)", code, combined.String())
	}
	if !strings.Contains(combined.String(), "\ncommand \"parse\" failed:") {
		t.Fatalf("expected error to start on new line, got %q", combined.String())
	}
}

func TestExecute_ParseSuccess_WritesDigitsArtifacts(t *testing.T) {
	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer

	baseDir := t.TempDir()
	inputPath := filepath.Join(baseDir, "sample.sup")
	outputDir := filepath.Join(baseDir, "out_digits")

	if writeErr := os.WriteFile(inputPath, sampleSUPPayload(), 0o644); writeErr != nil {
		t.Fatalf("failed to prepare input sup fixture: %v", writeErr)
	}

	code := root.Execute([]string{"parse", inputPath, "-o", outputDir}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, err.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", err.String())
	}

	for _, filename := range []string{"timeline.srt", "mapping.json", "sheet_0001.png"} {
		if _, statErr := os.Stat(filepath.Join(outputDir, filename)); statErr != nil {
			t.Fatalf("%s was not generated in digits mode: %v", filename, statErr)
		}
	}
	if !strings.Contains(out.String(), "Composing digits") {
		t.Fatalf("expected digits compose progress label, got %q", out.String())
	}
	if strings.Contains(out.String(), "Composing sheets") {
		t.Fatalf("digits mode should not report sheet compose label, got %q", out.String())
	}
}

func TestExecute_OCR_ArgumentErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		errMatch string
	}{
		{name: "missing output dir", args: []string{"ocr"}, errMatch: "ocr requires one output directory argument"},
		{name: "too many args", args: []string{"ocr", "out1", "out2"}, errMatch: "ocr requires exactly one output directory"},
		{name: "unknown flag", args: []string{"ocr", "out", "--bad"}, errMatch: "failed to parse ocr flags: unknown flag: --bad"},
		{name: "missing config value", args: []string{"ocr", "out", "--config"}, errMatch: "failed to parse ocr flags: flag needs an argument: --config"},
		{name: "removed input-mode flag", args: []string{"ocr", "out", "--input-mode", "digits"}, errMatch: "failed to parse ocr flags: unknown flag: --input-mode"},
		{name: "removed mode flag", args: []string{"ocr", "out", "--mode", "digits"}, errMatch: "failed to parse ocr flags: unknown flag: --mode"},
		{name: "removed write-new flag", args: []string{"ocr", "out", "--write-new"}, errMatch: "failed to parse ocr flags: unknown flag: --write-new"},
	}

	root := BuildRootCommand()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			var err bytes.Buffer
			code := root.Execute(tt.args, &out, &err)
			if code != 1 {
				t.Fatalf("expected exit code 1, got %d (stderr=%q)", code, err.String())
			}
			if !strings.Contains(err.String(), tt.errMatch) {
				t.Fatalf("expected stderr to include %q, got %q", tt.errMatch, err.String())
			}
		})
	}
}

func TestExecute_OCR_Success_WithInjectedRunner(t *testing.T) {
	original := runOCROnOutput
	t.Cleanup(func() { runOCROnOutput = original })

	runOCROnOutput = func(outputDir, configPath string, strict bool, progressCB ocr.ProgressFunc) (string, error) {
		if outputDir != "sample-out" {
			t.Fatalf("unexpected outputDir: %q", outputDir)
		}
		if configPath != "ocr_config.yaml" {
			t.Fatalf("unexpected configPath: %q", configPath)
		}
		if strict {
			t.Fatalf("expected strict=false by default")
		}
		if progressCB == nil {
			t.Fatalf("expected non-nil progress callback")
		}
		progressCB(0, 1, "")
		progressCB(1, 1, "sheet_0001.png")
		return filepath.Join(outputDir, "timeline.srt"), nil
	}

	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer
	code := root.Execute([]string{"ocr", "sample-out"}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, err.String())
	}
	if err.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", err.String())
	}
	if !strings.Contains(out.String(), "OCR timeline written to: sample-out") {
		t.Fatalf("expected success message, got %q", out.String())
	}
	if !strings.Contains(out.String(), "0% (0/1)") {
		t.Fatalf("expected OCR progress to start at 0/1, got %q", out.String())
	}
	if strings.Count(out.String(), "0% (0/1)") != 1 {
		t.Fatalf("expected exactly one initial OCR progress event, got %q", out.String())
	}
}

func TestExecute_OCR_StrictFlag_WithInjectedRunner(t *testing.T) {
	original := runOCROnOutput
	t.Cleanup(func() { runOCROnOutput = original })

	runOCROnOutput = func(outputDir, configPath string, strict bool, progressCB ocr.ProgressFunc) (string, error) {
		if !strict {
			t.Fatalf("expected strict=true when --strict is provided")
		}
		if progressCB == nil {
			t.Fatalf("expected non-nil progress callback")
		}
		progressCB(0, 1, "")
		progressCB(1, 1, "sheet_0001.png")
		return filepath.Join(outputDir, "timeline.srt"), nil
	}

	root := BuildRootCommand()
	var out bytes.Buffer
	var err bytes.Buffer
	code := root.Execute([]string{"ocr", "sample-out", "--strict"}, &out, &err)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, err.String())
	}
}

func writeSolidPNG(path string, w, h int) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func sampleSUPPayload() []byte {
	pcs := func(pts int, objectID int, paletteID int, x int, y int) []byte {
		body := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, byte(paletteID), 0x01}
		body = append(body, byte(objectID>>8), byte(objectID), 0x00, 0x00, byte(x>>8), byte(x), byte(y>>8), byte(y))
		return sampleSUPPacket(pts, 0x16, body)
	}
	pds := func(pts int, paletteID int, idx int, y int, cr int, cb int, alpha int) []byte {
		body := []byte{byte(paletteID), 0x00, byte(idx), byte(y), byte(cr), byte(cb), byte(alpha)}
		return sampleSUPPacket(pts, 0x14, body)
	}
	ods := func(pts int, objectID int, width int, height int, rle []byte) []byte {
		objDataLen := len(rle) + 4
		body := []byte{byte(objectID >> 8), byte(objectID), 0x00, 0xC0, byte(objDataLen >> 16), byte(objDataLen >> 8), byte(objDataLen), byte(width >> 8), byte(width), byte(height >> 8), byte(height)}
		body = append(body, rle...)
		return sampleSUPPacket(pts, 0x15, body)
	}
	end := func(pts int) []byte { return sampleSUPPacket(pts, 0x80, nil) }

	data := make([]byte, 0, 256)
	data = append(data, pds(0, 0, 1, 235, 128, 128, 255)...)
	data = append(data, ods(0, 1, 1, 1, []byte{0x01})...)
	data = append(data, pcs(90_000, 1, 0, 10, 20)...)
	data = append(data, end(90_000)...)
	data = append(data, pcs(180_000, 1, 0, 10, 20)...)
	data = append(data, end(180_000)...)
	return data
}

func sampleSUPPacket(pts int, segType byte, body []byte) []byte {
	out := make([]byte, 0, 13+len(body))
	out = append(out, 'P', 'G')
	out = append(out, byte(pts>>24), byte(pts>>16), byte(pts>>8), byte(pts))
	out = append(out, 0x00, 0x00, 0x00, 0x00)
	out = append(out, segType)
	out = append(out, byte(len(body)>>8), byte(len(body)))
	out = append(out, body...)
	return out
}
