package idxsub

import (
	"strings"
	"testing"
)

func TestExtractEntriesReturnsSortedEntries(t *testing.T) {
	text := strings.Join([]string{
		"timestamp: 00:00:02:000, filepos: 00000020",
		"timestamp: 00:00:01:000, filepos: 00000010",
	}, "\n")

	entries := ExtractEntries(text)
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}
	if entries[0].FilePos != 0x10 || entries[1].FilePos != 0x20 {
		t.Fatalf("filepos order = [%X, %X], want [10, 20]", entries[0].FilePos, entries[1].FilePos)
	}
}

func TestParsePaletteLineIgnoresInvalidTokens(t *testing.T) {
	palette := make([]uint32, 16)
	parsePaletteLine("palette: 000001, xyz, 0000FF, , FFFFFF", palette)

	if palette[0] != 0x000001 {
		t.Fatalf("palette[0] = %06X, want 000001", palette[0])
	}
	if palette[1] != 0x000000 {
		t.Fatalf("palette[1] = %06X, want 000000 for invalid token", palette[1])
	}
	if palette[2] != 0x0000FF {
		t.Fatalf("palette[2] = %06X, want 0000FF", palette[2])
	}
	if palette[3] != 0x000000 {
		t.Fatalf("palette[3] = %06X, want 000000 for empty token", palette[3])
	}
	if palette[4] != 0xFFFFFF {
		t.Fatalf("palette[4] = %06X, want FFFFFF", palette[4])
	}
}

func TestGetNibbleOutOfRangeReturnsZero(t *testing.T) {
	data := []byte{0xAB}
	if got := getNibble(data, -3); got != 0 {
		t.Fatalf("getNibble(-3) = %d, want 0", got)
	}
	if got := getNibble(data, 100); got != 0 {
		t.Fatalf("getNibble(100) = %d, want 0", got)
	}
}

func TestReadRunCodeExtendsShortCodes(t *testing.T) {
	// A zero nibble sequence forces the decoder through all extension branches
	// and returns the special fill-rest marker value (<4).
	packet := []byte{0x00, 0x00}
	got, next := readRunCode(packet, 0)
	if got != 0x0 {
		t.Fatalf("readRunCode value = 0x%X, want 0x0", got)
	}
	if next != 4 {
		t.Fatalf("readRunCode next nibble = %d, want 4", next)
	}
}

func TestApplyControlCommandHandlesShortAndUnknownCommands(t *testing.T) {
	control := &spuControl{}

	if _, ok := applyControlCommand(0x03, []byte{0x00}, 0, control); ok {
		t.Fatalf("applyControlCommand(0x03) with short packet should fail")
	}
	if _, ok := applyControlCommand(0x04, []byte{0x00}, 0, control); ok {
		t.Fatalf("applyControlCommand(0x04) with short packet should fail")
	}
	if _, ok := applyControlCommand(0x05, []byte{0x00, 0x01}, 0, control); ok {
		t.Fatalf("applyControlCommand(0x05) with short packet should fail")
	}
	if _, ok := applyControlCommand(0x06, []byte{0x00, 0x01}, 0, control); ok {
		t.Fatalf("applyControlCommand(0x06) with short packet should fail")
	}
	if _, ok := applyControlCommand(0x99, []byte{0x00, 0x01}, 0, control); ok {
		t.Fatalf("applyControlCommand(unknown) should fail")
	}
}

func TestSkipMPEG1HeaderVariants(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		want  int
		start int
		end   int
	}{
		{
			name:  "stuffing and std buffer with pts only",
			data:  []byte{0xFF, 0xFF, 0x40, 0x00, 0x21, 0, 0, 0, 0, 0},
			want:  9,
			start: 0,
			end:   10,
		},
		{
			name:  "pts and dts branch",
			data:  []byte{0x30, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			want:  10,
			start: 0,
			end:   10,
		},
		{
			name:  "mpeg1 private stream marker branch",
			data:  []byte{0x0F, 0xAA, 0xBB},
			want:  1,
			start: 0,
			end:   3,
		},
		{
			name:  "insufficient bytes for pts only returns end",
			data:  []byte{0x21, 0x00, 0x00},
			want:  3,
			start: 0,
			end:   3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipMPEG1Header(tc.data, tc.start, tc.end); got != tc.want {
				t.Fatalf("skipMPEG1Header() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGetPESPayloadBoundsRejectsShortPayload(t *testing.T) {
	// Marker + length is present, but payload has fewer than 3 bytes.
	data := []byte{0x00, 0x00, 0x01, 0xBD, 0x00, 0x02, 0x11, 0x22}
	_, _, ok := getPESPayloadBounds(data, 0, len(data))
	if ok {
		t.Fatalf("getPESPayloadBounds should reject payload shorter than 3 bytes")
	}
}

func TestResolvePayloadStartEmptyRangeReturnsEnd(t *testing.T) {
	data := []byte{0x80, 0x00, 0x00}
	if got := resolvePayloadStart(data, 2, 2); got != 2 {
		t.Fatalf("resolvePayloadStart empty range = %d, want 2", got)
	}
}

func TestParseCuesAndFramesErrorBranches(t *testing.T) {
	t.Run("filepos out of range", func(t *testing.T) {
		idxText := "timestamp: 00:00:01:000, filepos: 0000000A"
		_, err := ParseCuesAndFrames(idxText, []byte{0x00, 0x01, 0x02})
		if err == nil || !strings.Contains(err.Error(), "filepos out of range") {
			t.Fatalf("ParseCuesAndFrames error = %v, want filepos out of range", err)
		}
	})

	t.Run("decode packet too short", func(t *testing.T) {
		// The extracted SPU payload has declared size 2, so decodeSPUPacket must fail.
		shortSPU := []byte{0x00, 0x02}
		subData := buildMPEG1PES(0x20, shortSPU)
		idxText := "timestamp: 00:00:01:000, filepos: 00000000"

		_, err := ParseCuesAndFrames(idxText, subData)
		if err == nil || !strings.Contains(err.Error(), "SPU packet too short") {
			t.Fatalf("ParseCuesAndFrames error = %v, want SPU packet too short", err)
		}
	})
}

func TestDecodeSPUPacketValidationErrors(t *testing.T) {
	palette := make([]uint32, 16)

	t.Run("incomplete packet size", func(t *testing.T) {
		_, err := decodeSPUPacket([]byte{0x00, 0x10, 0x00, 0x04}, palette)
		if err == nil || !strings.Contains(err.Error(), "incomplete SPU packet") {
			t.Fatalf("decodeSPUPacket error = %v, want incomplete SPU packet", err)
		}
	})

	t.Run("missing bitmap offsets", func(t *testing.T) {
		packet := []byte{
			0x00, 0x14, // size
			0x00, 0x08, // control offset
			0x00, 0x00, // bitmap placeholder
			0x00, 0x00, // padding
			0x00, 0x00, 0x00, 0x08, // control header
			0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x02, // valid display area
			0xFF, // no 0x06 command => missing offsets
		}
		_, err := decodeSPUPacket(packet, palette)
		if err == nil || !strings.Contains(err.Error(), "missing VobSub bitmap offsets") {
			t.Fatalf("decodeSPUPacket error = %v, want missing VobSub bitmap offsets", err)
		}
	})

	t.Run("bitmap offsets out of range", func(t *testing.T) {
		packet := syntheticSPUPacket()
		packet[21] = 0x01 // offset1 high byte => 0x0104
		packet[22] = 0x04
		_, err := decodeSPUPacket(packet, palette)
		if err == nil || !strings.Contains(err.Error(), "VobSub bitmap offsets out of range") {
			t.Fatalf("decodeSPUPacket error = %v, want offsets out of range", err)
		}
	})
}
