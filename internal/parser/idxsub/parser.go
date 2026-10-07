package idxsub

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"math"
	"sort"
	"strconv"
	"strings"

	"pixelsup-go/internal/model"
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
// Behavior is intentionally aligned with the Python parser used during migration:
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
// This keeps the migration parser robust on mixed-quality IDX files while still
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

func getNibble(data []byte, nibbleIdx int) int {
	byteIdx := nibbleIdx / 2
	if byteIdx < 0 || byteIdx >= len(data) {
		return 0
	}
	b := data[byteIdx]
	if nibbleIdx&1 == 1 {
		return int(b & 0x0F)
	}
	return int(b >> 4)
}

func readRunCode(packet []byte, nibbleIdx int) (int, int) {
	v := 0
	for t := 1; v < t && t <= 0x40; t <<= 2 {
		v = (v << 4) | getNibble(packet, nibbleIdx)
		nibbleIdx++
	}
	return v, nibbleIdx
}

// decodeRLEFieldToRGBAIndexed decodes one VobSub field directly into RGBA
// output using FFmpeg-compatible 2-bit run decoding and row byte alignment.
// rowOffset points to the first destination row, while rowStride controls how
// to move between decoded rows (for interlaced fields this is frame.Stride*2).
func decodeRLEFieldToRGBAIndexed(
	frame *image.RGBA,
	width int,
	rows int,
	packet []byte,
	offsetBytes int,
	rowOffset int,
	rowStride int,
	rgbaByIndex [4][4]uint8,
) error {
	nibbleIdx := offsetBytes * 2
	nibbleEnd := len(packet) * 2

	for row := 0; row < rows; row++ {
		scanStart := rowOffset + row*rowStride
		x := 0
		for x < width {
			if nibbleIdx >= nibbleEnd {
				return fmt.Errorf("RLE bitstream ended early")
			}
			v, nextNibble := readRunCode(packet, nibbleIdx)
			nibbleIdx = nextNibble
			run := v >> 2
			color := byte(v & 0x03)
			if v < 4 {
				run = math.MaxInt32 // fill rest of line
			}
			if run != math.MaxInt32 && run > width-x {
				return fmt.Errorf("RLE run overflow: run=%d remain=%d", run, width-x)
			}
			if run > width-x {
				run = width - x
			}
			// scanStart is a byte offset at the beginning of the row, while x is in pixels.
			// Convert x to bytes to keep RGBA writes aligned to pixel boundaries.
			fillStart := scanStart + x*4
			fillEnd := fillStart + run*4
			if fillStart < 0 || fillEnd > len(frame.Pix) {
				return fmt.Errorf("RLE write out of bitmap bounds")
			}
			c := rgbaByIndex[color]
			off := fillStart
			switch run {
			case 1:
				frame.Pix[off+0] = c[0]
				frame.Pix[off+1] = c[1]
				frame.Pix[off+2] = c[2]
				frame.Pix[off+3] = c[3]
			case 2:
				frame.Pix[off+0] = c[0]
				frame.Pix[off+1] = c[1]
				frame.Pix[off+2] = c[2]
				frame.Pix[off+3] = c[3]
				off += 4
				frame.Pix[off+0] = c[0]
				frame.Pix[off+1] = c[1]
				frame.Pix[off+2] = c[2]
				frame.Pix[off+3] = c[3]
			default:
				for i := 0; i < run; i++ {
					frame.Pix[off+0] = c[0]
					frame.Pix[off+1] = c[1]
					frame.Pix[off+2] = c[2]
					frame.Pix[off+3] = c[3]
					off += 4
				}
			}
			x += run
		}
		if nibbleIdx&1 == 1 {
			nibbleIdx++
		}
	}
	return nil
}

func applyControlCommand(cmd byte, packet []byte, pos int, control *spuControl) (int, bool) {
	switch cmd {
	case 0x00, 0x01, 0x02:
		return pos, true
	case 0x03:
		if pos+2 > len(packet) {
			return pos, false
		}
		b0, b1 := packet[pos], packet[pos+1]
		control.colormap[3] = b0 >> 4
		control.colormap[2] = b0 & 0x0F
		control.colormap[1] = b1 >> 4
		control.colormap[0] = b1 & 0x0F
		return pos + 2, true
	case 0x04:
		if pos+2 > len(packet) {
			return pos, false
		}
		b0, b1 := packet[pos], packet[pos+1]
		control.alpha[3] = b0 >> 4
		control.alpha[2] = b0 & 0x0F
		control.alpha[1] = b1 >> 4
		control.alpha[0] = b1 & 0x0F
		return pos + 2, true
	case 0x05:
		if pos+6 > len(packet) {
			return pos, false
		}
		b0, b1, b2, b3, b4, b5 := packet[pos], packet[pos+1], packet[pos+2], packet[pos+3], packet[pos+4], packet[pos+5]
		control.x1 = int(b0)<<4 | int(b1>>4)
		control.x2 = int(b1&0x0F)<<8 | int(b2)
		control.y1 = int(b3)<<4 | int(b4>>4)
		control.y2 = int(b4&0x0F)<<8 | int(b5)
		return pos + 6, true
	case 0x06:
		if pos+4 > len(packet) {
			return pos, false
		}
		control.offset1 = int(packet[pos])<<8 | int(packet[pos+1])
		control.offset2 = int(packet[pos+2])<<8 | int(packet[pos+3])
		return pos + 4, true
	default:
		return pos, false
	}
}

func decodeSPUPacket(packet []byte, palette []uint32) (*image.RGBA, error) {
	if len(packet) < 4 {
		return nil, fmt.Errorf("SPU packet too short: %d", len(packet))
	}

	packetSize := int(packet[0])<<8 | int(packet[1])
	ctrlOffset := int(packet[2])<<8 | int(packet[3])
	if packetSize > len(packet) {
		return nil, fmt.Errorf("incomplete SPU packet: expected %d bytes, got %d", packetSize, len(packet))
	}
	packet = packet[:packetSize]

	control := spuControl{
		colormap: [4]uint8{0, 1, 2, 3},
		alpha:    [4]uint8{0xF, 0xF, 0xF, 0xF},
	}

	cmdPos := ctrlOffset
	for cmdPos > 0 && cmdPos < len(packet) {
		if cmdPos+4 > len(packet) {
			break
		}
		nextCmd := int(packet[cmdPos+2])<<8 | int(packet[cmdPos+3])
		pos := cmdPos + 4

		for pos < len(packet) {
			cmd := packet[pos]
			pos++
			if cmd == 0xFF {
				break
			}

			nextPos, handled := applyControlCommand(cmd, packet, pos, &control)
			pos = nextPos
			if !handled {
				break
			}
		}

		if nextCmd <= cmdPos || nextCmd >= len(packet) {
			break
		}
		cmdPos = nextCmd
	}

	width := control.x2 - control.x1 + 1
	height := control.y2 - control.y1 + 1
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid VobSub dimensions: width=%d height=%d", width, height)
	}
	if control.offset1 <= 0 || control.offset2 <= 0 {
		return nil, fmt.Errorf("missing VobSub bitmap offsets")
	}
	if control.offset1 >= len(packet) || control.offset2 >= len(packet) {
		return nil, fmt.Errorf("VobSub bitmap offsets out of range: %d %d", control.offset1, control.offset2)
	}

	// FFmpeg decodes DVD subtitle fields into an indexed bitmap with interlaced
	// line stride (w*2). We mirror the same field layout directly in RGBA
	// memory to avoid an intermediate indexed bitmap allocation.
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	rgbaByIndex := [4][4]uint8{}
	for pix := range rgbaByIndex {
		palIdx := control.colormap[pix] & 0x0F
		rgb := uint32(0xFFFFFF)
		if int(palIdx) < len(palette) {
			rgb = palette[palIdx]
		}

		rgbaByIndex[pix][0] = uint8((rgb >> 16) & 0xFF)
		rgbaByIndex[pix][1] = uint8((rgb >> 8) & 0xFF)
		rgbaByIndex[pix][2] = uint8(rgb & 0xFF)
		rgbaByIndex[pix][3] = (control.alpha[pix] & 0x0F) * 17
	}
	if err := decodeRLEFieldToRGBAIndexed(frame, width, (height+1)/2, packet, control.offset1, 0, frame.Stride*2, rgbaByIndex); err != nil {
		return nil, fmt.Errorf("decode top field: %w", err)
	}
	if err := decodeRLEFieldToRGBAIndexed(frame, width, height/2, packet, control.offset2, frame.Stride, frame.Stride*2, rgbaByIndex); err != nil {
		return nil, fmt.Errorf("decode bottom field: %w", err)
	}
	return frame, nil
}

func findPESMarker(data []byte, cursor int, end int) int {
	if cursor < 0 {
		cursor = 0
	}
	if end > len(data) {
		end = len(data)
	}
	if end-cursor < len(privateStreamMarkerBytes) {
		return -1
	}
	rel := bytes.Index(data[cursor:end], privateStreamMarkerBytes)
	if rel < 0 {
		return -1
	}
	return cursor + rel
}

func getPESPayloadBounds(data []byte, marker int, end int) (int, int, bool) {
	if marker+6 > end {
		return 0, 0, false
	}

	pesLen := int(data[marker+4])<<8 | int(data[marker+5])
	payloadEnd := end
	if pesLen > 0 {
		payloadEnd = marker + 6 + pesLen
		if payloadEnd > end {
			payloadEnd = end
		}
	}

	payloadStart := marker + 6
	if payloadStart+3 > payloadEnd {
		return 0, 0, false
	}
	return payloadStart, payloadEnd, true
}

func skipMPEG2Header(data []byte, payloadStart int, payloadEnd int) int {
	headerLen := int(data[payloadStart+2])
	start := payloadStart + 3 + headerLen
	if start > payloadEnd {
		return payloadEnd
	}
	return start
}

func skipMPEG1Header(data []byte, payloadStart int, payloadEnd int) int {
	p := payloadStart
	for p < payloadEnd && data[p] == 0xFF {
		p++
	}
	if p < payloadEnd && (data[p]&0xC0) == 0x40 {
		p += 2
	}
	if p < payloadEnd && (data[p]&0xF0) == 0x20 {
		if p+5 > payloadEnd {
			return payloadEnd
		}
		return p + 5
	}
	if p < payloadEnd && (data[p]&0xF0) == 0x30 {
		if p+10 > payloadEnd {
			return payloadEnd
		}
		return p + 10
	}
	if p < payloadEnd && data[p] == 0x0F {
		if p+1 > payloadEnd {
			return payloadEnd
		}
		return p + 1
	}
	return p
}

func resolvePayloadStart(data []byte, payloadStart int, payloadEnd int) int {
	if payloadStart >= payloadEnd {
		return payloadEnd
	}
	if (data[payloadStart] & 0xC0) == 0x80 {
		return skipMPEG2Header(data, payloadStart, payloadEnd)
	}
	return skipMPEG1Header(data, payloadStart, payloadEnd)
}

// extractSPUPacket merges private stream chunks belonging to DVD subtitle
// substreams (0x20..0x3F) and returns one complete SPU packet.
func extractSPUPacket(subData []byte, start int, end int) ([]byte, error) {
	return extractSPUPacketForSubstream(subData, start, end, 0)
}

// extractSPUPacketForSubstream merges private-stream subtitle chunks and returns
// one complete SPU packet. When targetSubstreamID is non-zero, only that DVD
// subtitle substream id (0x20..0x3F) is accepted; otherwise all subtitle
// substreams are accepted (legacy behavior used by low-level tests).
func extractSPUPacketForSubstream(subData []byte, start int, end int, targetSubstreamID byte) ([]byte, error) {
	if start < 0 {
		start = 0
	}
	if end > len(subData) {
		end = len(subData)
	}
	if end < start {
		end = start
	}

	// Merge subtitle substream chunks (0x20..0x3F) in one pass over the byte span.
	// This avoids the intermediate [][]byte + bytes.Join pattern and stops early
	// once the SPU packet's declared length is fully assembled.
	merged := make([]byte, 0, end-start)
	expectedSize := 0
	cursor := start

	for cursor+6 <= end {
		marker := findPESMarker(subData, cursor, end)
		if marker < 0 {
			break
		}

		pesPayloadStart, pesPayloadEnd, ok := getPESPayloadBounds(subData, marker, end)
		if !ok {
			cursor = marker + 4
			continue
		}

		payloadStart := resolvePayloadStart(subData, pesPayloadStart, pesPayloadEnd)
		if payloadStart >= pesPayloadEnd {
			cursor = marker + 4
			continue
		}

		payload := subData[payloadStart:pesPayloadEnd]
		if len(payload) == 0 {
			cursor = marker + 4
			continue
		}

		substreamID := payload[0]
		if substreamID >= 0x20 && substreamID <= 0x3F && len(payload) > 1 &&
			(targetSubstreamID == 0 || substreamID == targetSubstreamID) {
			merged = append(merged, payload[1:]...)
			if expectedSize == 0 && len(merged) >= 2 {
				expectedSize = int(merged[0])<<8 | int(merged[1])
			}
			if expectedSize > 0 && len(merged) >= expectedSize {
				return merged[:expectedSize], nil
			}
		}

		// Move to current PES end to avoid repeatedly scanning the same payload.
		cursor = pesPayloadEnd
	}

	if len(merged) < 2 {
		return nil, fmt.Errorf("%w at idx filepos", errNoSubtitlePayload)
	}

	if expectedSize > len(merged) {
		return nil, fmt.Errorf("incomplete subtitle payload between idx offsets")
	}

	return merged[:expectedSize], nil
}
