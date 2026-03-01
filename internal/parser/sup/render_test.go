package sup

import "testing"

// TestRenderObject_PaletteAndMissingEntriesParity validates the rendered RGBA bytes
// for a small object containing both mapped and unmapped palette indices. This
// test locks the exact compatibility behavior: missing indices must stay fully
// transparent, while mapped indices must match YCbCr->RGBA conversion output.
func TestRenderObject_PaletteAndMissingEntriesParity(t *testing.T) {
	defn := objectDefinition{
		width:   3,
		height:  1,
		rleData: []byte{0x01, 0x02, 0x03, 0x00, 0x00},
	}

	palette := map[uint8]paletteEntry{
		1: {y: 235, cb: 128, cr: 128, alpha: 255},
		2: {y: 16, cb: 128, cr: 128, alpha: 255},
		// Index 3 intentionally missing to verify transparent fallback behavior.
	}

	got := renderObject(defn, palette)
	if got.Bounds().Dx() != 3 || got.Bounds().Dy() != 1 {
		t.Fatalf("rendered bounds = %v, want 3x1", got.Bounds())
	}

	r1, g1, b1, a1 := ycbcrToRGBA(235, 128, 128, 255)
	r2, g2, b2, a2 := ycbcrToRGBA(16, 128, 128, 255)

	want := []byte{
		byte(r1), byte(g1), byte(b1), byte(a1),
		byte(r2), byte(g2), byte(b2), byte(a2),
		0, 0, 0, 0,
	}
	if len(got.Pix) != len(want) {
		t.Fatalf("len(Pix) = %d, want %d", len(got.Pix), len(want))
	}

	for i := range want {
		if got.Pix[i] != want[i] {
			t.Fatalf("Pix[%d] = %d, want %d", i, got.Pix[i], want[i])
		}
	}
}
