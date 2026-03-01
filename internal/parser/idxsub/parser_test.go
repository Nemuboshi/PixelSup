package idxsub

import (
	"image"
	"strings"
	"testing"
)

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantMS  int
		wantErr bool
	}{
		{
			name:   "valid basic format",
			raw:    "01:02:03:456",
			wantMS: 3_723_456,
		},
		{
			name:   "valid with outer whitespace",
			raw:    " 00:00:01:000\t",
			wantMS: 1_000,
		},
		{
			name:    "invalid separators",
			raw:     "00:00:01.000",
			wantErr: true,
		},
		{
			name:    "invalid width",
			raw:     "0:00:01:000",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTimestamp(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTimestamp(%q) error = nil, want non-nil", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTimestamp(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.wantMS {
				t.Fatalf("ParseTimestamp(%q) = %d, want %d", tc.raw, got, tc.wantMS)
			}
		})
	}
}

func TestParseTimestampLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   Entry
		wantOK bool
	}{
		{
			name:   "common idx timestamp line",
			line:   "timestamp: 00:00:01:000, filepos: 0000000A",
			want:   Entry{StartMS: 1_000, FilePos: 0xA},
			wantOK: true,
		},
		{
			name:   "prefix is case-insensitive",
			line:   "TiMeStAmP: 00:00:02:500, filepos: 00000010",
			want:   Entry{StartMS: 2_500, FilePos: 0x10},
			wantOK: true,
		},
		{
			name:   "non timestamp line ignored",
			line:   "palette: 000000, ffffff",
			wantOK: false,
		},
		{
			name:   "timestamp line without filepos skipped",
			line:   "timestamp: 00:00:03:000",
			wantOK: false,
		},
		{
			name:   "timestamp line with malformed time skipped",
			line:   "timestamp: 00:00:3:000, filepos: 00000020",
			wantOK: false,
		},
		{
			name:   "uppercase FILEPOS token is skipped to match python behavior",
			line:   "timestamp: 00:00:04:000, FILEPOS: 00000030",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseTimestampLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ParseTimestampLine(%q) ok = %v, want %v", tc.line, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got != tc.want {
				t.Fatalf("ParseTimestampLine(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestParseIDXParsesPaletteAndSortsEntries(t *testing.T) {
	text := strings.Join([]string{
		"# comment",
		"palette: 000000, FF0000, 00FF00, 0000FF",
		"timestamp: 00:00:10:000, filepos: 00000030",
		"timestamp: 00:00:05:000, filepos: 00000010",
		"timestamp: 00:00:07:000, filepos: 00000020",
	}, "\n")

	palette, entries := ParseIDX(text)
	if len(palette) != 16 {
		t.Fatalf("palette len = %d, want 16", len(palette))
	}
	if palette[1] != 0xFF0000 || palette[2] != 0x00FF00 || palette[3] != 0x0000FF {
		t.Fatalf("palette parsed values mismatch: %06X %06X %06X", palette[1], palette[2], palette[3])
	}
	if len(entries) != 3 {
		t.Fatalf("entries len = %d, want 3", len(entries))
	}

	wantOrder := []int{0x10, 0x20, 0x30}
	for i, want := range wantOrder {
		if entries[i].FilePos != want {
			t.Fatalf("entries[%d].FilePos = 0x%X, want 0x%X", i, entries[i].FilePos, want)
		}
	}
}

func TestParseLangIndex(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{
			name: "valid langidx",
			text: "langidx: 4",
			want: 4,
		},
		{
			name: "missing langidx defaults to zero",
			text: "palette: 000000, ffffff",
			want: 0,
		},
		{
			name: "negative value clamps to zero",
			text: "langidx: -3",
			want: 0,
		},
		{
			name: "value above range clamps to 31",
			text: "langidx: 99",
			want: 31,
		},
		{
			name: "malformed value ignored",
			text: "langidx: x",
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseLangIndex(tc.text); got != tc.want {
				t.Fatalf("ParseLangIndex() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReadRunCode(t *testing.T) {
	packet := []byte{0x90}
	got, next := readRunCode(packet, 0)
	if got != 0x09 {
		t.Fatalf("readRunCode value = 0x%X, want 0x09", got)
	}
	if next != 1 {
		t.Fatalf("readRunCode next nibble = %d, want 1", next)
	}
}

func TestDecodeRLEFieldToRGBAIndexedFillsRow(t *testing.T) {
	frame := image.NewRGBA(image.Rect(0, 0, 2, 2))
	rgbaByPixel := [4][4]uint8{
		{1, 2, 3, 4},
		{10, 11, 12, 13},
		{20, 21, 22, 23},
		{30, 31, 32, 33},
	}
	// Mirror decodeSPUPacket default fill semantics for untouched pixels.
	base := rgbaByPixel[0]
	for off := 0; off+3 < len(frame.Pix); off += 4 {
		frame.Pix[off+0] = base[0]
		frame.Pix[off+1] = base[1]
		frame.Pix[off+2] = base[2]
		frame.Pix[off+3] = base[3]
	}

	packet := []byte{0x90}
	if err := decodeRLEFieldToRGBAIndexed(frame, 2, 1, packet, 0, 0, frame.Stride, rgbaByPixel); err != nil {
		t.Fatalf("decodeRLEFieldToRGBAIndexed error: %v", err)
	}

	if got := frame.RGBAAt(0, 0); got.R != 10 || got.G != 11 || got.B != 12 || got.A != 13 {
		t.Fatalf("pixel (0,0) = %#v, want rgbaByPixel[1]", got)
	}
	if got := frame.RGBAAt(1, 0); got.R != 10 || got.G != 11 || got.B != 12 || got.A != 13 {
		t.Fatalf("pixel (1,0) = %#v, want rgbaByPixel[1]", got)
	}
	if got := frame.RGBAAt(0, 1); got.R != 1 || got.G != 2 || got.B != 3 || got.A != 4 {
		t.Fatalf("pixel (0,1) = %#v, want untouched base color", got)
	}
}

func TestExtractSPUPacketFromPrivateStream(t *testing.T) {
	packet := syntheticSPUPacket()
	subData := syntheticPES(packet)

	got, err := extractSPUPacket(subData, 0, len(subData))
	if err != nil {
		t.Fatalf("extractSPUPacket error: %v", err)
	}
	if len(got) != len(packet) {
		t.Fatalf("packet len = %d, want %d", len(got), len(packet))
	}
	for i := range packet {
		if got[i] != packet[i] {
			t.Fatalf("packet byte[%d] = 0x%X, want 0x%X", i, got[i], packet[i])
		}
	}
}

func TestParseCuesAndFramesSyntheticEndToEnd(t *testing.T) {
	idxText := strings.Join([]string{
		"palette: 000000, FF0000, 00FF00, 0000FF",
		"timestamp: 00:00:01:000, filepos: 00000000",
	}, "\n")
	subData := syntheticPES(syntheticSPUPacket())

	rendered, err := ParseCuesAndFrames(idxText, subData)
	if err != nil {
		t.Fatalf("ParseCuesAndFrames error: %v", err)
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered cue count = %d, want 1", len(rendered))
	}

	cue := rendered[0].Cue
	if cue.Index != 1 || cue.StartMS != 1_000 || cue.EndMS != 3_000 {
		t.Fatalf("cue mismatch: %+v", cue)
	}

	frame := rendered[0].Frame
	if frame.Bounds().Dx() != 2 || frame.Bounds().Dy() != 2 {
		t.Fatalf("frame bounds = %v, want 2x2", frame.Bounds())
	}

	// First row should decode to palette index 1 (red), second row to index 2 (green).
	r0 := frame.RGBAAt(0, 0)
	r1 := frame.RGBAAt(0, 1)
	if r0.R != 0xFF || r0.G != 0x00 || r0.B != 0x00 || r0.A != 0xFF {
		t.Fatalf("row0 color = %#v, want red opaque", r0)
	}
	if r1.R != 0x00 || r1.G != 0xFF || r1.B != 0x00 || r1.A != 0xFF {
		t.Fatalf("row1 color = %#v, want green opaque", r1)
	}
}

func TestParseCuesAndFramesUsesLangIndexSubstream(t *testing.T) {
	idxText := strings.Join([]string{
		"langidx: 1",
		"palette: 000000, FF0000, 00FF00, 0000FF",
		"timestamp: 00:00:01:000, filepos: 00000000",
	}, "\n")

	// Build one PES packet at stream 0x20 (wrong stream for langidx=1) and one
	// at stream 0x21 (expected stream). The parser must choose 0x21.
	wrongPacket := syntheticSPUPacket()
	wrongPacket[5] = 0x41 // second row red too (acts as a detectable mismatch)

	rightPacket := syntheticSPUPacket() // row0 red, row1 green

	subData := append(syntheticPESWithSubstream(0x20, wrongPacket), syntheticPESWithSubstream(0x21, rightPacket)...)
	rendered, err := ParseCuesAndFrames(idxText, subData)
	if err != nil {
		t.Fatalf("ParseCuesAndFrames error: %v", err)
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered cue count = %d, want 1", len(rendered))
	}

	frame := rendered[0].Frame
	r0 := frame.RGBAAt(0, 0)
	r1 := frame.RGBAAt(0, 1)
	if r0.R != 0xFF || r0.G != 0x00 || r0.B != 0x00 {
		t.Fatalf("row0 color = %#v, want red", r0)
	}
	if r1.R != 0x00 || r1.G != 0xFF || r1.B != 0x00 {
		t.Fatalf("row1 color = %#v, want green from selected langidx stream", r1)
	}
}

func TestDecodeSPUPacketPreservesRunBoundariesWithinRow(t *testing.T) {
	// This packet encodes a single-row subtitle where the row has two runs:
	// first two pixels use color index 1 (red), next two use color index 2 (green).
	// The test guards against byte/pixel offset bugs when x > 0 inside a row.
	packet := syntheticSPUPacketSingleRowTwoRuns()
	palette := make([]uint32, 16)
	palette[1] = 0xFF0000
	palette[2] = 0x00FF00

	frame, err := decodeSPUPacket(packet, palette)
	if err != nil {
		t.Fatalf("decodeSPUPacket error: %v", err)
	}
	if frame.Bounds().Dx() != 4 || frame.Bounds().Dy() != 1 {
		t.Fatalf("frame bounds = %v, want 4x1", frame.Bounds())
	}

	want := []struct {
		x, y int
		r, g, b uint8
	}{
		{0, 0, 0xFF, 0x00, 0x00},
		{1, 0, 0xFF, 0x00, 0x00},
		{2, 0, 0x00, 0xFF, 0x00},
		{3, 0, 0x00, 0xFF, 0x00},
	}
	for _, tc := range want {
		got := frame.RGBAAt(tc.x, tc.y)
		if got.R != tc.r || got.G != tc.g || got.B != tc.b || got.A != 0xFF {
			t.Fatalf("pixel(%d,%d) = %#v, want rgba(%d,%d,%d,255)", tc.x, tc.y, got, tc.r, tc.g, tc.b)
		}
	}
}

func syntheticSPUPacket() []byte {
	// This packet encodes a 2x2 subtitle image with two field rows:
	// - even row uses nibble code 0x9 (run=2, color index 1)
	// - odd row uses nibble code 0xA (run=2, color index 2)
	return []byte{
		0x00, 0x19, // packet size = 25 bytes
		0x00, 0x08, // control sequence offset
		0x90,       // offset1 bitmap data (row 0)
		0xA0,       // offset2 bitmap data (row 1)
		0x00, 0x00, // padding before control section
		0x00, 0x00, 0x00, 0x08, // control header (next control points to self)
		0x05,                               // set display area
		0x00, 0x00, 0x01, 0x00, 0x00, 0x01, // x1=0,x2=1,y1=0,y2=1 => width=2,height=2
		0x06,       // set bitmap offsets
		0x00, 0x04, // offset1
		0x00, 0x05, // offset2
		0xFF, // end commands
	}
}

func syntheticSPUPacketSingleRowTwoRuns() []byte {
	// Row data: 0x9 => run=2,color=1; 0xA => run=2,color=2.
	// Two nibbles fit in one byte.
	field1 := []byte{0x9A}
	offset1 := 4
	offset2 := 5      // valid in-range offset; odd field has zero rows for height=1.

	packet := []byte{
		0x00, 0x19, // packet size = 25 bytes
		0x00, 0x08, // control sequence offset
		field1[0],  // field 1 bitmap data
		0x00,       // padding byte at offset2
		0x00, 0x00, // extra padding before control section
		0x00, 0x08, 0x00, 0x08, // control header (self loop)
		0x05,                               // set display area
		0x00, 0x00, 0x03, 0x00, 0x00, 0x00, // x1=0,x2=3,y1=0,y2=0 => 4x1
		0x06, // set bitmap offsets
		0x00, byte(offset1),
		0x00, byte(offset2),
		0xFF, // end commands
	}
	return packet
}

func syntheticPES(spuPacket []byte) []byte {
	return syntheticPESWithSubstream(0x20, spuPacket)
}

func syntheticPESWithSubstream(substreamID byte, spuPacket []byte) []byte {
	payloadLen := len(spuPacket) + 2 // MPEG1 0x0F header + substream id
	data := []byte{
		0x00, 0x00, 0x01, 0xBD, // private stream marker
		byte(payloadLen >> 8), byte(payloadLen), // PES payload length
		0x0F, // MPEG1 header marker: payload starts at next byte
		substreamID, // subtitle substream id
	}
	data = append(data, spuPacket...)
	return data
}

func BenchmarkDecodeSPUPacket(b *testing.B) {
	packet := syntheticSPUPacketRect(64, 48)
	palette := make([]uint32, 16)
	palette[1] = 0xFF0000
	palette[2] = 0x00FF00

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		frame, err := decodeSPUPacket(packet, palette)
		if err != nil {
			b.Fatalf("decodeSPUPacket error: %v", err)
		}
		if frame.Bounds().Dx() != 64 || frame.Bounds().Dy() != 48 {
			b.Fatalf("unexpected frame bounds: %v", frame.Bounds())
		}
	}
}

func syntheticSPUPacketRect(width int, height int) []byte {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}

	encodeRow := func(color byte) []byte {
		// Build one row as repeated run=1 codes. For 2-bit RLE, a one-pixel run
		// is encoded as a single nibble: v=(1<<2)|color.
		nibbles := make([]byte, width)
		code := byte(0x04 | (color & 0x03))
		for i := range nibbles {
			nibbles[i] = code
		}
		if len(nibbles)%2 == 1 {
			nibbles = append(nibbles, 0)
		}
		row := make([]byte, len(nibbles)/2)
		for i := 0; i < len(nibbles); i += 2 {
			row[i/2] = (nibbles[i] << 4) | nibbles[i+1]
		}
		return row
	}

	evenRows := (height + 1) / 2
	oddRows := height / 2
	evenRowData := encodeRow(1)
	oddRowData := encodeRow(2)
	field1 := make([]byte, 0, len(evenRowData)*evenRows)
	field2 := make([]byte, 0, len(oddRowData)*oddRows)
	for i := 0; i < evenRows; i++ {
		field1 = append(field1, evenRowData...)
	}
	for i := 0; i < oddRows; i++ {
		field2 = append(field2, oddRowData...)
	}

	offset1 := 4
	offset2 := offset1 + len(field1)
	ctrlOffset := 4 + len(field1) + len(field2)

	x1, x2 := 0, width-1
	y1, y2 := 0, height-1
	b0 := byte((x1 >> 4) & 0xFF)
	b1 := byte((x1&0x0F)<<4 | ((x2 >> 8) & 0x0F))
	b2 := byte(x2 & 0xFF)
	b3 := byte((y1 >> 4) & 0xFF)
	b4 := byte((y1&0x0F)<<4 | ((y2 >> 8) & 0x0F))
	b5 := byte(y2 & 0xFF)

	packet := []byte{
		0x00, 0x00, // packet size placeholder
		byte(ctrlOffset >> 8), byte(ctrlOffset), // control sequence offset
	}
	packet = append(packet, field1...)
	packet = append(packet, field2...)
	packet = append(packet,
		byte(ctrlOffset>>8), byte(ctrlOffset), byte(ctrlOffset>>8), byte(ctrlOffset), // control header
		0x05, b0, b1, b2, b3, b4, b5, // display area
		0x06, byte(offset1>>8), byte(offset1), byte(offset2>>8), byte(offset2), // bitmap offsets
		0xFF, // end commands
	)

	packetSize := len(packet)
	packet[0] = byte(packetSize >> 8)
	packet[1] = byte(packetSize)
	return packet
}
