package sup

import "testing"

func TestU16AndU24(t *testing.T) {
	data := []byte{0x12, 0x34, 0x56, 0x78}
	if got := u16(data, 0); got != 0x1234 {
		t.Fatalf("u16(data,0) = %#x, want %#x", got, 0x1234)
	}
	if got := u24(data, 1); got != 0x345678 {
		t.Fatalf("u24(data,1) = %#x, want %#x", got, 0x345678)
	}
}

func TestYCbCrToRGBA_ClampsOutput(t *testing.T) {
	r, g, b, a := ycbcrToRGBA(16, 128, 128, 200)
	if r != 0 || g != 0 || b != 0 || a != 200 {
		t.Fatalf("neutral ycbcr conversion mismatch: got (%d,%d,%d,%d)", r, g, b, a)
	}

	// Extreme values should be clamped to [0,255].
	r2, g2, b2, _ := ycbcrToRGBA(255, 255, 255, 255)
	if r2 < 0 || r2 > 255 || g2 < 0 || g2 > 255 || b2 < 0 || b2 > 255 {
		t.Fatalf("rgb values out of range after clamp: (%d,%d,%d)", r2, g2, b2)
	}
}
