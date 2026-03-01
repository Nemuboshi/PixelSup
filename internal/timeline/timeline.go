package timeline

import (
	"encoding/json"
	"fmt"
	"os"

	"pixelsup-go/internal/model"
)

// mappingItem is the serialized shape used by mapping.json.
type mappingItem struct {
	CueIndex        int    `json:"cue_index"`
	StartMS         int    `json:"start_ms"`
	EndMS           int    `json:"end_ms"`
	Sheet           string `json:"sheet"`
	PositionInSheet int    `json:"position_in_sheet"`
}

// mappingPayload is the root structure for mapping.json output.
type mappingPayload struct {
	Items []mappingItem `json:"items"`
}

// FormatSRTTimestamp converts millisecond time to SRT timestamp format.
// It follows HH:MM:SS,mmm and clamps negative values to 0.
func FormatSRTTimestamp(ms int) string {
	if ms < 0 {
		ms = 0
	}

	hh := ms / 3_600_000
	ms %= 3_600_000
	mm := ms / 60_000
	ms %= 60_000
	ss := ms / 1_000
	ms %= 1_000

	return fmt.Sprintf("%02d:%02d:%02d,%03d", hh, mm, ss, ms)
}

// WriteSRT writes timeline.srt with image marker text, mirroring current Python behavior.
// It fails fast if any cue index has no placement mapping.
func WriteSRT(cues []model.SubtitleCue, placements map[int]model.CuePlacement, outPath string) error {
	lines := make([]string, 0, len(cues)*4)
	for i, cue := range cues {
		placement, ok := placements[cue.Index]
		if !ok {
			return fmt.Errorf("missing placement for cue index %d", cue.Index)
		}
		lines = append(lines, fmt.Sprintf("%d", i+1))
		lines = append(lines, fmt.Sprintf("%s --> %s", FormatSRTTimestamp(cue.StartMS), FormatSRTTimestamp(cue.EndMS)))
		lines = append(lines, fmt.Sprintf("[img:%s#%02d]", placement.SheetName, placement.PositionInSheet))
		lines = append(lines, "")
	}

	content := []byte(joinLines(lines))
	return os.WriteFile(outPath, content, 0o644)
}

// WriteMappingJSON writes mapping.json used by downstream OCR and tooling.
// The function preserves cue order as provided by the caller.
func WriteMappingJSON(cues []model.SubtitleCue, placements map[int]model.CuePlacement, outPath string) error {
	items := make([]mappingItem, 0, len(cues))
	for _, cue := range cues {
		placement, ok := placements[cue.Index]
		if !ok {
			return fmt.Errorf("missing placement for cue index %d", cue.Index)
		}
		items = append(items, mappingItem{
			CueIndex:        cue.Index,
			StartMS:         cue.StartMS,
			EndMS:           cue.EndMS,
			Sheet:           placement.SheetName,
			PositionInSheet: placement.PositionInSheet,
		})
	}

	payload := mappingPayload{Items: items}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mapping.json: %w", err)
	}
	return os.WriteFile(outPath, b, 0o644)
}

// joinLines is intentionally tiny and allocation-friendly for deterministic output.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	n := 0
	for _, line := range lines {
		n += len(line) + 1
	}
	buf := make([]byte, 0, n)
	for i, line := range lines {
		buf = append(buf, line...)
		if i < len(lines)-1 {
			buf = append(buf, '\n')
		}
	}
	return string(buf)
}
