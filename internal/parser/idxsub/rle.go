package idxsub

import (
	"fmt"
	"image"
	"math"
)

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
