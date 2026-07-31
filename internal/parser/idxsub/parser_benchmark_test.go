package idxsub

import "testing"

func BenchmarkDecodeRLEFieldToRGBA(b *testing.B) {
	const (
		width  = 320
		height = 120
	)
	// With FFmpeg-compatible 2-bit RLE decode, nibble 0x5 means run=1,color=1.
	// Two nibbles per byte => each 0x55 byte paints two pixels.
	rowBytes := width / 2
	rowCount := (height + 1) / 2

	packet := make([]byte, rowBytes*rowCount)
	for i := range packet {
		packet[i] = 0x55
	}
	frame, _ := decodeSPUPacket(syntheticSPUPacketRect(width, height), []uint32{
		0x000000, 0xFFFFFF, 0x00FF00, 0x0000FF,
	})
	rgbaByPixel := [4][4]uint8{
		{0, 0, 0, 255},
		{255, 0, 0, 255},
		{0, 255, 0, 255},
		{0, 0, 255, 255},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clear(frame.Pix)
		if err := decodeRLEFieldToRGBAIndexed(frame, width, rowCount, packet, 0, 0, frame.Stride*2, rgbaByPixel); err != nil {
			b.Fatalf("decodeRLEFieldToRGBAIndexed error: %v", err)
		}
	}
}

func BenchmarkDecodeSPUPacketLarge(b *testing.B) {
	packet := syntheticSPUPacketRect(320, 120)
	palette := make([]uint32, 16)
	for i := range palette {
		// Use distinct palette values so the loop always touches all RGB assignment paths.
		palette[i] = uint32(i)<<16 | uint32(i)<<8 | uint32(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frame, err := decodeSPUPacket(packet, palette)
		if err != nil {
			b.Fatalf("decodeSPUPacket error: %v", err)
		}
		if frame.Rect.Dx() != 320 || frame.Rect.Dy() != 120 {
			b.Fatalf("frame size = %dx%d, want 320x120", frame.Rect.Dx(), frame.Rect.Dy())
		}
	}
}

func BenchmarkExtractSPUPacket(b *testing.B) {
	packet := syntheticSPUPacketRect(320, 120)
	chunks := splitIntoChunks(packet, 512)
	streamParts := make([][]byte, 0, len(chunks)+4)
	streamParts = append(streamParts, []byte{0xBA, 0xAD, 0xF0, 0x0D})
	for i, chunk := range chunks {
		if i%2 == 0 {
			streamParts = append(streamParts, buildMPEG1PES(0x20, chunk))
		} else {
			streamParts = append(streamParts, buildMPEG2PES(0x20, []byte{0x11, 0x22}, chunk))
		}
	}
	// Add a decoy packet to ensure the hot path keeps filtering non-subtitle substream ids.
	streamParts = append(streamParts, buildMPEG1PES(0x10, []byte{0x01, 0x02, 0x03}))
	stream := mergeChunks(streamParts)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := extractSPUPacket(stream, 0, len(stream))
		if err != nil {
			b.Fatalf("extractSPUPacket error: %v", err)
		}
		if len(got) != len(packet) {
			b.Fatalf("packet len = %d, want %d", len(got), len(packet))
		}
	}
}
