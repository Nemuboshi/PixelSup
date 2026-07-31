package sup

// u16 reads a big-endian unsigned 16-bit integer from data at the given offset.
// Callers are expected to provide a valid offset; this is a low-level hot path helper.
func u16(data []byte, off int) int {
	return int(data[off])<<8 | int(data[off+1])
}

// u24 reads a big-endian unsigned 24-bit integer from data at the given offset.
// PGS object headers use this compact width for object data length.
func u24(data []byte, off int) int {
	return int(data[off])<<16 | int(data[off+1])<<8 | int(data[off+2])
}

// ycbcrToRGBA converts PGS palette YCbCr+Alpha into clamped RGBA.
// The conversion formula matches the Python reference implementation to keep
// output parity stable during migration.
func ycbcrToRGBA(y, cb, cr, alpha int) (int, int, int, int) {
	c := y - 16
	d := cb - 128
	e := cr - 128

	r := (298*c + 409*e + 128) >> 8
	g := (298*c - 100*d - 208*e + 128) >> 8
	b := (298*c + 516*d + 128) >> 8

	if r < 0 {
		r = 0
	}
	if r > 255 {
		r = 255
	}
	if g < 0 {
		g = 0
	}
	if g > 255 {
		g = 255
	}
	if b < 0 {
		b = 0
	}
	if b > 255 {
		b = 255
	}
	return r, g, b, alpha
}
