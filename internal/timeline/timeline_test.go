package timeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pixelsup-go/internal/model"
)

func TestFormatSRTTimestamp(t *testing.T) {
	got := FormatSRTTimestamp(3723456)
	want := "01:02:03,456"
	if got != want {
		t.Fatalf("FormatSRTTimestamp() = %q, want %q", got, want)
	}
}

func TestWriteSRT(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "timeline.srt")

	cues := []model.SubtitleCue{
		{Index: 1, StartMS: 0, EndMS: 1000},
		{Index: 2, StartMS: 1000, EndMS: 2500},
	}
	placements := map[int]model.CuePlacement{
		1: {SheetName: "sheet_0001.png", PositionInSheet: 1},
		2: {SheetName: "sheet_0001.png", PositionInSheet: 2},
	}

	if err := WriteSRT(cues, placements, out); err != nil {
		t.Fatalf("WriteSRT() error = %v", err)
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	text := string(b)
	if !strings.Contains(text, "00:00:00,000 --> 00:00:01,000") {
		t.Fatalf("missing first cue timing in SRT: %s", text)
	}
	if !strings.Contains(text, "[img:sheet_0001.png#02]") {
		t.Fatalf("missing expected marker in SRT: %s", text)
	}
}

func TestWriteMappingJSON(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "mapping.json")

	cues := []model.SubtitleCue{
		{Index: 7, StartMS: 100, EndMS: 500},
	}
	placements := map[int]model.CuePlacement{
		7: {SheetName: "sheet_0009.png", PositionInSheet: 3},
	}

	if err := WriteMappingJSON(cues, placements, out); err != nil {
		t.Fatalf("WriteMappingJSON() error = %v", err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	items, ok := parsed["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("mapping items malformed: %#v", parsed["items"])
	}
}
