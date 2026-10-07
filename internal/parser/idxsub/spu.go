package idxsub

import (
	"fmt"
	"image"
)

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
