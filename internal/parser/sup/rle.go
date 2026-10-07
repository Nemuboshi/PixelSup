package sup

// DecodeRLEIndices decodes a PGS (Blu-ray SUP) RLE payload into a flat
// row-major palette-index buffer with exactly width*height items.
//
// Important behavioral details (kept compatible with the Python version):
//  1. PGS line boundaries are explicit; byte sequence 00 00 means end-of-line.
//  2. If a line ends before width pixels are emitted, the decoder pads the
//     remainder with transparent index 0.
//  3. If malformed/short input truncates output, the decoder pads missing tail
//     pixels with index 0 so callers always get stable dimensions.
//  4. If emitted pixels exceed current line width, extra pixels are ignored
//     until the next explicit end-of-line marker.
func DecodeRLEIndices(data []byte, width, height int) []uint8 {
	if width <= 0 || height <= 0 {
		return []uint8{}
	}

	need := width * height
	// Preallocate the full output and write by index to avoid repeated append
	// growth checks in the decoder hot path.
	out := make([]uint8, need)
	write := 0
	x := 0
	y := 0
	i := 0

	for i < len(data) && y < height {
		b := data[i]
		i++
		if b != 0 {
			if x < width {
				out[write] = b
				write++
				x++
			}
			continue
		}

		if i >= len(data) {
			break
		}
		b2 := data[i]
		i++

		if b2 == 0 {
			if x < width {
				// Rows are zero-initialized, so padding only needs an index advance.
				write += width - x
			}
			x = 0
			y++
			continue
		}

		var run int
		var color uint8
		switch {
		case b2 < 0x40:
			// 00 xx: transparent run length in one-byte form.
			run = int(b2)
			color = 0
		case b2 < 0x80:
			// 00 4x yy: transparent run with 10-bit length.
			if i >= len(data) {
				break
			}
			run = int(b2-0x40)<<8 + int(data[i])
			i++
			color = 0
		case b2 < 0xC0:
			// 00 8x cc: colored run with 6-bit length.
			if i >= len(data) {
				break
			}
			run = int(b2 - 0x80)
			color = data[i]
			i++
		default:
			// 00 Cx yy cc: colored run with 10-bit length.
			if i+1 >= len(data) {
				break
			}
			run = int(b2-0xC0)<<8 + int(data[i])
			color = data[i+1]
			i += 2
		}

		for r := 0; r < run; r++ {
			if y >= height {
				break
			}
			if x >= width {
				// PGS streams rely on explicit EOL markers; do not wrap implicitly.
				continue
			}
			out[write] = color
			write++
			x++
		}
	}

	// If the current line is partially emitted, close it like the Python decoder.
	if y < height && x > 0 {
		write += width - x
		y++
	}

	// Any unread tail remains transparent due to zero initialization.
	return out
}
