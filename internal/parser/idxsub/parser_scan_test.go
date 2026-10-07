package idxsub

import (
	"bytes"
	"strings"
	"testing"
)

func TestExtractSPUPacketMergesFragmentsAcrossPES(t *testing.T) {
	spu := syntheticSPUPacket()
	chunks := splitIntoChunks(spu, 7)
	if len(chunks) < 3 {
		t.Fatalf("chunking produced %d chunks, want at least 3", len(chunks))
	}

	stream := bytes.Join([][]byte{
		{0xDE, 0xAD, 0xBE, 0xEF},
		buildMPEG1PES(0x20, chunks[0]),
		buildMPEG1PES(0x1F, []byte{0xAA, 0xBB, 0xCC}), // non-subtitle substream; should be skipped.
		buildMPEG2PES(0x20, []byte{0xCA, 0xFE}, chunks[1]),
		// Include trailing bytes in final chunk; extraction should stop at declared SPU size.
		buildMPEG1PES(0x20, append(mergeChunks(chunks[2:]), 0x55, 0x66)),
		buildMPEG1PES(0x20, []byte{0x99, 0x88, 0x77}), // should never be needed once packet is complete.
	}, nil)

	got, err := extractSPUPacket(stream, 0, len(stream))
	if err != nil {
		t.Fatalf("extractSPUPacket error: %v", err)
	}

	if !bytes.Equal(got, spu) {
		t.Fatalf("SPU payload mismatch: got % X, want % X", got, spu)
	}
}

func TestExtractSPUPacketRejectsIncompleteMergedPayload(t *testing.T) {
	spu := syntheticSPUPacket()
	partial := spu[:10]

	stream := buildMPEG1PES(0x20, partial)
	_, err := extractSPUPacket(stream, 0, len(stream))
	if err == nil {
		t.Fatalf("extractSPUPacket error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "incomplete subtitle payload") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "incomplete subtitle payload")
	}
}

func TestExtractSPUPacketForSubstreamFiltersLanguages(t *testing.T) {
	want := syntheticSPUPacket()
	wrong := append([]byte{}, want...)
	wrong[5] = 0x41 // make second row color different for easy mismatch detection

	stream := bytes.Join([][]byte{
		buildMPEG1PES(0x20, wrong),
		buildMPEG1PES(0x24, want),
	}, nil)

	got, err := extractSPUPacketForSubstream(stream, 0, len(stream), 0x24)
	if err != nil {
		t.Fatalf("extractSPUPacketForSubstream error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("SPU payload mismatch for filtered substream: got % X, want % X", got, want)
	}
}

// buildMPEG1PES builds a private stream PES packet with the simple MPEG-1 0x0F
// header style that the parser recognizes in skipMPEG1Header.
func buildMPEG1PES(substreamID byte, payload []byte) []byte {
	pesPayload := append([]byte{0x0F, substreamID}, payload...)
	pesLen := len(pesPayload)
	out := []byte{
		0x00, 0x00, 0x01, 0xBD,
		byte(pesLen >> 8), byte(pesLen),
	}
	out = append(out, pesPayload...)
	return out
}

// buildMPEG2PES builds a private stream PES packet with an MPEG-2 style header.
// The parser should skip the header bytes and return payload starting at substream id.
func buildMPEG2PES(substreamID byte, extraHeader []byte, payload []byte) []byte {
	pesPayload := []byte{0x80, 0x00, byte(len(extraHeader))}
	pesPayload = append(pesPayload, extraHeader...)
	pesPayload = append(pesPayload, substreamID)
	pesPayload = append(pesPayload, payload...)
	pesLen := len(pesPayload)

	out := []byte{
		0x00, 0x00, 0x01, 0xBD,
		byte(pesLen >> 8), byte(pesLen),
	}
	out = append(out, pesPayload...)
	return out
}

func splitIntoChunks(data []byte, chunkSize int) [][]byte {
	if chunkSize <= 0 {
		return [][]byte{append([]byte{}, data...)}
	}
	out := make([][]byte, 0, (len(data)+chunkSize-1)/chunkSize)
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		out = append(out, append([]byte{}, data[i:end]...))
	}
	return out
}

func mergeChunks(chunks [][]byte) []byte {
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	merged := make([]byte, 0, total)
	for _, chunk := range chunks {
		merged = append(merged, chunk...)
	}
	return merged
}
