package idxsub

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var (
	// errNoSubtitlePayload indicates the byte span has no subtitle packet for
	// the requested filtering mode.
	errNoSubtitlePayload = errors.New("no subtitle payload found")
)

const (
	privateStreamMarker = "\x00\x00\x01\xbd"
	defaultCueDuration  = 2_000
	minCueDuration      = 500
)

var privateStreamMarkerBytes = []byte(privateStreamMarker)

// Entry stores the minimal IDX cue metadata needed for later .sub packet lookup.
type Entry struct {
	StartMS int
	FilePos int
}

type spuControl struct {
	colormap [4]uint8
	alpha    [4]uint8
	x1       int
	x2       int
	y1       int
	y2       int
	offset1  int
	offset2  int
}

// ParseTimestamp converts an IDX timestamp string (HH:MM:SS:MMM) to milliseconds.
// The input is trimmed first; any non-exact format is rejected with an error.
func ParseTimestamp(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != 12 || trimmed[2] != ':' || trimmed[5] != ':' || trimmed[8] != ':' {
		return 0, fmt.Errorf("invalid idx timestamp: %q", raw)
	}
	hh, ok := parseDec2(trimmed[0], trimmed[1])
	if !ok {
		return 0, fmt.Errorf("invalid idx timestamp: %q", raw)
	}
	mm, ok := parseDec2(trimmed[3], trimmed[4])
	if !ok {
		return 0, fmt.Errorf("invalid idx timestamp: %q", raw)
	}
	ss, ok := parseDec2(trimmed[6], trimmed[7])
	if !ok {
		return 0, fmt.Errorf("invalid idx timestamp: %q", raw)
	}
	ms, ok := parseDec3(trimmed[9], trimmed[10], trimmed[11])
	if !ok {
		return 0, fmt.Errorf("invalid idx timestamp: %q", raw)
	}
	return hh*3_600_000 + mm*60_000 + ss*1_000 + ms, nil
}

// ParseTimestampLine extracts an IDX entry from a single text line.
//
// Behavior is intentionally aligned with the established IDX parser semantics:
// - only lines that start with "timestamp:" (case-insensitive) are considered
// - timestamp and filepos are searched within that line
// - malformed timestamp lines are skipped without returning an error
func ParseTimestampLine(rawLine string) (Entry, bool) {
	line := strings.TrimSpace(rawLine)
	if !hasPrefixFold(line, "timestamp:") {
		return Entry{}, false
	}

	startMS, ok := findTimestampMS(line)
	if !ok {
		return Entry{}, false
	}
	filePos, ok := findFilePos(line)
	if !ok {
		return Entry{}, false
	}
	filePos64, err := strconv.ParseInt(filePos, 16, 64)
	if err != nil {
		return Entry{}, false
	}

	return Entry{
		StartMS: startMS,
		FilePos: int(filePos64),
	}, true
}

// ParseIDX parses a full IDX text payload and returns:
// - a 16-color RGB palette used by VobSub rendering
// - sorted timestamp/filepos entries
//
// Palette parsing intentionally tolerates malformed tokens by ignoring them.
// This keeps the parser robust on mixed-quality IDX files while still
// producing deterministic defaults for missing colors.
func ParseIDX(text string) ([]uint32, []Entry) {
	palette, entries, _ := parseIDXCore(text)
	return palette, entries
}

// parseIDXCore performs a single-pass IDX scan and returns palette, sorted
// entries, and langidx. Keeping these three outputs together avoids repeated
// full-text scans when callers need both cue metadata and language stream info.
func parseIDXCore(text string) ([]uint32, []Entry, int) {
	palette := make([]uint32, 16)
	entries := make([]Entry, 0)
	langIdx := 0

	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if hasPrefixFold(line, "palette:") {
			parsePaletteLine(line, palette)
			continue
		}
		if hasPrefixFold(line, "langidx:") {
			if parsed, ok := parseLangIdxLine(line); ok {
				langIdx = parsed
			}
			continue
		}

		if hasPrefixFold(line, "timestamp:") {
			entry, ok := ParseTimestampLine(line)
			if ok {
				entries = append(entries, entry)
			}
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].FilePos < entries[j].FilePos
	})
	return palette, entries, langIdx
}

// ParseLangIndex extracts the active VobSub language index from IDX text.
// The returned value is clamped to the DVD subtitle substream range [0, 31].
// If the field is missing or malformed, stream 0 is used as a stable default.
func ParseLangIndex(text string) int {
	langIdx := 0
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if !hasPrefixFold(line, "langidx:") {
			continue
		}
		if parsed, ok := parseLangIdxLine(line); ok {
			langIdx = parsed
		}
	}
	return langIdx
}

// parseLangIdxLine parses one "langidx:" line and clamps to subtitle substream
// range [0,31]. It returns ok=false for malformed values.
func parseLangIdxLine(line string) (int, bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, false
	}
	if value < 0 {
		return 0, true
	}
	if value > 31 {
		return 31, true
	}
	return value, true
}

// hasPrefixFold checks ASCII/Unicode case-insensitive prefix matching without
// allocating a lower-cased copy of the full line.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// ExtractEntries parses all timestamp/filepos pairs from IDX text and returns
// them sorted by filepos.
func ExtractEntries(text string) []Entry {
	_, entries := ParseIDX(text)
	return entries
}

// ParseCuesAndFrames decodes IDX metadata and matching SUB payload bytes into
// rendered subtitle frames. The function is file-I/O free by design so callers
// can load bytes from any source and keep deterministic tests.
