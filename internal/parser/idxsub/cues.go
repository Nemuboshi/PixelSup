package idxsub

import (
	"errors"
	"fmt"
	"pixelsup-go/internal/model"
	"strconv"
	"strings"
)

func ParseCuesAndFrames(idxText string, subData []byte) ([]model.RenderedCue, error) {
	palette, entries, langIdx := parseIDXCore(idxText)
	langSubstreamID := byte(0x20 + langIdx)
	if len(entries) == 0 {
		return []model.RenderedCue{}, nil
	}

	cues := make([]model.RenderedCue, 0, len(entries))
	for i, entry := range entries {
		start := entry.FilePos
		if start < 0 || start >= len(subData) {
			return nil, fmt.Errorf("idx entry %d filepos out of range: %d", i, start)
		}

		end := len(subData)
		if i+1 < len(entries) && entries[i+1].FilePos < end {
			end = entries[i+1].FilePos
		}
		if end < start {
			end = start
		}

		packet, err := extractSPUPacketForSubstream(subData, start, end, langSubstreamID)
		if err != nil && errors.Is(err, errNoSubtitlePayload) {
			// Some streams carry sparse packets for a selected language; when the
			// preferred stream id is absent in this span, fall back to any subtitle
			// substream to preserve legacy tolerance instead of hard-failing.
			packet, err = extractSPUPacket(subData, start, end)
		}
		if err != nil {
			return nil, fmt.Errorf("extract SPU packet for entry %d: %w", i, err)
		}

		frame, err := decodeSPUPacket(packet, palette)
		if err != nil {
			return nil, fmt.Errorf("decode SPU packet for entry %d: %w", i, err)
		}

		endMS := entry.StartMS + defaultCueDuration
		if i+1 < len(entries) {
			endMS = entries[i+1].StartMS
		}
		if endMS <= entry.StartMS {
			endMS = entry.StartMS + minCueDuration
		}

		cues = append(cues, model.RenderedCue{
			Cue: model.SubtitleCue{
				Index:   i + 1,
				StartMS: entry.StartMS,
				EndMS:   endMS,
			},
			Frame: frame,
		})
	}

	return cues, nil
}

func parsePaletteLine(line string, palette []uint32) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 || colon+1 >= len(line) {
		return
	}
	payload := line[colon+1:]
	colorIdx := 0
	start := 0
	for i := 0; i <= len(payload) && colorIdx < len(palette); i++ {
		if i < len(payload) && payload[i] != ',' {
			continue
		}
		token := strings.TrimSpace(payload[start:i])
		if token != "" {
			rgb, err := strconv.ParseUint(token, 16, 32)
			if err == nil {
				palette[colorIdx] = uint32(rgb)
			}
		}
		colorIdx++
		start = i + 1
	}
}

func parseDec2(a byte, b byte) (int, bool) {
	if a < '0' || a > '9' || b < '0' || b > '9' {
		return 0, false
	}
	return int(a-'0')*10 + int(b-'0'), true
}

func parseDec3(a byte, b byte, c byte) (int, bool) {
	if a < '0' || a > '9' || b < '0' || b > '9' || c < '0' || c > '9' {
		return 0, false
	}
	return int(a-'0')*100 + int(b-'0')*10 + int(c-'0'), true
}

// findTimestampMS scans a line for the first HH:MM:SS:MMM pattern and converts
// it to milliseconds.
func findTimestampMS(line string) (int, bool) {
	for i := 0; i+11 < len(line); i++ {
		if line[i+2] != ':' || line[i+5] != ':' || line[i+8] != ':' {
			continue
		}
		hh, ok := parseDec2(line[i], line[i+1])
		if !ok {
			continue
		}
		mm, ok := parseDec2(line[i+3], line[i+4])
		if !ok {
			continue
		}
		ss, ok := parseDec2(line[i+6], line[i+7])
		if !ok {
			continue
		}
		ms, ok := parseDec3(line[i+9], line[i+10], line[i+11])
		if !ok {
			continue
		}
		return hh*3_600_000 + mm*60_000 + ss*1_000 + ms, true
	}
	return 0, false
}

// findFilePos extracts the lowercase "filepos:" token value. The lookup is
// intentionally case-sensitive to preserve Python parser compatibility.
func findFilePos(line string) (string, bool) {
	idx := strings.Index(line, "filepos:")
	if idx < 0 {
		return "", false
	}
	i := idx + len("filepos:")
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	start := i
	for i < len(line) {
		c := line[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			i++
			continue
		}
		break
	}
	if i == start {
		return "", false
	}
	return line[start:i], true
}
