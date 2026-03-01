package sup

import "testing"

func TestDecodeRLEIndices_ExplicitEndOfLine_NoBlankRows(t *testing.T) {
	// Two lines, each with 4 pixels of color index 1, each line terminated by 00 00.
	data := []byte{0x00, 0x84, 0x01, 0x00, 0x00, 0x00, 0x84, 0x01, 0x00, 0x00}
	got := DecodeRLEIndices(data, 4, 2)
	want := []uint8{1, 1, 1, 1, 1, 1, 1, 1}

	if len(got) != len(want) {
		t.Fatalf("decoded length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeRLEIndices_PadsShortRowsToTransparent(t *testing.T) {
	// One row, width 4, but only one pixel emitted then end-of-line.
	data := []byte{0x01, 0x00, 0x00}
	got := DecodeRLEIndices(data, 4, 1)
	want := []uint8{1, 0, 0, 0}

	if len(got) != len(want) {
		t.Fatalf("decoded length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeRLEIndices_AllRunForms(t *testing.T) {
	// Row 1: 00 xx transparent run.
	// Row 2: 00 4x yy transparent run.
	// Row 3: 00 8x cc colored run.
	// Row 4: 00 Cx yy cc colored run.
	data := []byte{
		0x00, 0x02, 0x07, 0x00, 0x00,
		0x00, 0x40, 0x03, 0x09, 0x00, 0x00,
		0x00, 0x82, 0x05, 0x00, 0x00,
		0x00, 0xC0, 0x03, 0x06, 0x00, 0x00,
	}
	got := DecodeRLEIndices(data, 4, 4)
	want := []uint8{
		0, 0, 7, 0,
		0, 0, 0, 9,
		5, 5, 0, 0,
		6, 6, 6, 0,
	}

	if len(got) != len(want) {
		t.Fatalf("decoded length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeRLEIndices_TruncatedExtendedRunPadsTail(t *testing.T) {
	// 00 40 indicates a transparent 10-bit run but misses its low-byte length.
	got := DecodeRLEIndices([]byte{0x00, 0x40}, 3, 1)
	want := []uint8{0, 0, 0}

	if len(got) != len(want) {
		t.Fatalf("decoded length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeRLEIndices_IgnoresRunOverflowPastRowWidth(t *testing.T) {
	// Length 6 run into width-4 row should keep first 4 pixels and ignore overflow.
	data := []byte{0x00, 0x86, 0x04, 0x00, 0x00}
	got := DecodeRLEIndices(data, 4, 1)
	want := []uint8{4, 4, 4, 4}

	if len(got) != len(want) {
		t.Fatalf("decoded length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeRLEIndices_EmptyForNonPositiveDimensions(t *testing.T) {
	if got := DecodeRLEIndices([]byte{0x01}, 0, 2); len(got) != 0 {
		t.Fatalf("width=0 decoded length = %d, want 0", len(got))
	}
	if got := DecodeRLEIndices([]byte{0x01}, 2, -1); len(got) != 0 {
		t.Fatalf("height<0 decoded length = %d, want 0", len(got))
	}
}
