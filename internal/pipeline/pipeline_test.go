package pipeline

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pixelsup-go/internal/compose"
)

type progressEvent struct {
	stage       string
	done, total int
}

type testMapping struct {
	Items []struct {
		CueIndex        int    `json:"cue_index"`
		StartMS         int    `json:"start_ms"`
		EndMS           int    `json:"end_ms"`
		Sheet           string `json:"sheet"`
		PositionInSheet int    `json:"position_in_sheet"`
	} `json:"items"`
}

func TestParseImageDirectoryWritesArtifactsAndReportsProgress(t *testing.T) {
	root := t.TempDir()
	inputDir := filepath.Join(root, "frames")
	outputDir := filepath.Join(root, "out")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, filepath.Join(inputDir, "1.png"))
	writeTestPNG(t, filepath.Join(inputDir, "2.png"))

	var events []progressEvent
	result, err := Parse(ParseOptions{
		Input:    inputDir,
		Output:   outputDir,
		Limit:    2,
		MaxWidth: 64,
		Padding:  1,
		Layout:   compose.LayoutNoRowIndex,
	}, func(stage string, done, total int) {
		events = append(events, progressEvent{stage: stage, done: done, total: total})
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.OutputDir != outputDir {
		t.Fatalf("output dir = %q, want %q", result.OutputDir, outputDir)
	}
	if result.Count != 1 {
		t.Fatalf("sheet count = %d, want 1", result.Count)
	}

	for _, name := range []string{"sheet_0001.png", "timeline.srt", "mapping.json"} {
		assertFileExists(t, filepath.Join(outputDir, name))
	}

	mapping := readTestMapping(t, filepath.Join(outputDir, "mapping.json"))
	if len(mapping.Items) != 2 {
		t.Fatalf("mapping items = %d, want 2", len(mapping.Items))
	}
	if got := mapping.Items[0]; got.CueIndex != 1 || got.StartMS != 0 || got.EndMS != 1000 || got.Sheet != "sheet_0001.png" || got.PositionInSheet != 1 {
		t.Fatalf("unexpected first mapping item: %+v", got)
	}
	if got := mapping.Items[1]; got.CueIndex != 2 || got.StartMS != 1000 || got.EndMS != 2000 || got.Sheet != "sheet_0001.png" || got.PositionInSheet != 2 {
		t.Fatalf("unexpected second mapping item: %+v", got)
	}

	srt, err := os.ReadFile(filepath.Join(outputDir, "timeline.srt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(srt)
	for _, want := range []string{
		"00:00:00,000 --> 00:00:01,000",
		"[img:sheet_0001.png#01]",
		"00:00:01,000 --> 00:00:02,000",
		"[img:sheet_0001.png#02]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("timeline missing %q:\n%s", want, text)
		}
	}

	assertProgressCompleted(t, events, "Loading input", 1)
	assertProgressCompleted(t, events, "Preprocessing", 2)
	assertProgressCompleted(t, events, "Composing sheets", 1)
	assertProgressCompleted(t, events, "Writing sheets", 1)
}

func TestParseRejectsImageNumberGap(t *testing.T) {
	root := t.TempDir()
	inputDir := filepath.Join(root, "frames")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, filepath.Join(inputDir, "1.png"))
	writeTestPNG(t, filepath.Join(inputDir, "3.png"))

	_, err := Parse(ParseOptions{
		Input:    inputDir,
		Output:   filepath.Join(root, "out"),
		Limit:    2,
		MaxWidth: 64,
		Layout:   compose.LayoutNoRowIndex,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "missing number: 2") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRemovesStaleGeneratedArtifacts(t *testing.T) {
	root := t.TempDir()
	inputDir := filepath.Join(root, "frames")
	outputDir := filepath.Join(root, "out")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outputDir, "temp"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, filepath.Join(inputDir, "1.png"))
	for _, path := range []string{
		filepath.Join(outputDir, "sheet_9999.png"),
		filepath.Join(outputDir, "timeline.srt"),
		filepath.Join(outputDir, "mapping.json"),
		filepath.Join(outputDir, "temp", "stale.txt"),
	} {
		if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := Parse(ParseOptions{
		Input:    inputDir,
		Output:   outputDir,
		Limit:    1,
		MaxWidth: 64,
		Layout:   compose.LayoutNoRowIndex,
	}, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "sheet_9999.png")); !os.IsNotExist(err) {
		t.Fatalf("stale sheet still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "temp")); !os.IsNotExist(err) {
		t.Fatalf("stale temp directory still exists: %v", err)
	}
	assertFileExists(t, filepath.Join(outputDir, "sheet_0001.png"))
}

func TestExportSUPWritesCueArtifactsAndReportsProgress(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "sample.sup")
	outputDir := filepath.Join(root, "export")
	if err := os.WriteFile(inputPath, sampleSUPPayload(), 0o644); err != nil {
		t.Fatal(err)
	}

	var events []progressEvent
	result, err := Export(ExportOptions{Input: inputPath, Output: outputDir}, func(stage string, done, total int) {
		events = append(events, progressEvent{stage: stage, done: done, total: total})
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.OutputDir != outputDir {
		t.Fatalf("output dir = %q, want %q", result.OutputDir, outputDir)
	}
	if result.Count != 2 {
		t.Fatalf("cue count = %d, want 2", result.Count)
	}

	for _, name := range []string{"cue_00001.png", "cue_00002.png", "timeline.srt", "mapping.json"} {
		assertFileExists(t, filepath.Join(outputDir, name))
	}

	mapping := readTestMapping(t, filepath.Join(outputDir, "mapping.json"))
	if len(mapping.Items) != 2 {
		t.Fatalf("mapping items = %d, want 2", len(mapping.Items))
	}
	if got := mapping.Items[0]; got.CueIndex != 1 || got.StartMS != 1000 || got.EndMS != 2000 || got.Sheet != "cue_00001.png" || got.PositionInSheet != 1 {
		t.Fatalf("unexpected first export mapping item: %+v", got)
	}
	if got := mapping.Items[1]; got.CueIndex != 2 || got.StartMS != 2000 || got.EndMS != 4000 || got.Sheet != "cue_00002.png" || got.PositionInSheet != 1 {
		t.Fatalf("unexpected second export mapping item: %+v", got)
	}

	assertProgressCompleted(t, events, "Loading input", 1)
	assertProgressCompleted(t, events, "Writing cues", 2)
}

func TestExportRejectsDirectoryInput(t *testing.T) {
	inputDir := t.TempDir()
	_, err := Export(ExportOptions{Input: inputDir, Output: filepath.Join(t.TempDir(), "out")}, nil)
	if err == nil || !strings.Contains(err.Error(), "export input must be .sup or .idx") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertProgressCompleted(t *testing.T, events []progressEvent, stage string, total int) {
	t.Helper()
	for _, event := range events {
		if event.stage == stage && event.done == total && event.total == total {
			return
		}
	}
	t.Fatalf("progress never completed stage %q at %d/%d: %+v", stage, total, total, events)
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	} else if info.IsDir() {
		t.Fatalf("expected file, got directory: %s", path)
	}
}

func readTestMapping(t *testing.T, path string) testMapping {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var mapping testMapping
	if err := json.Unmarshal(raw, &mapping); err != nil {
		t.Fatalf("decode mapping: %v", err)
	}
	return mapping
}

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
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
