package idxsub

import (
	"bytes"
	"fmt"
)

func findPESMarker(data []byte, cursor int, end int) int {
	if cursor < 0 {
		cursor = 0
	}
	if end > len(data) {
		end = len(data)
	}
	if end-cursor < len(privateStreamMarkerBytes) {
		return -1
	}
	rel := bytes.Index(data[cursor:end], privateStreamMarkerBytes)
	if rel < 0 {
		return -1
	}
	return cursor + rel
}

func getPESPayloadBounds(data []byte, marker int, end int) (int, int, bool) {
	if marker+6 > end {
		return 0, 0, false
	}

	pesLen := int(data[marker+4])<<8 | int(data[marker+5])
	payloadEnd := end
	if pesLen > 0 {
		payloadEnd = marker + 6 + pesLen
		if payloadEnd > end {
			payloadEnd = end
		}
	}

	payloadStart := marker + 6
	if payloadStart+3 > payloadEnd {
		return 0, 0, false
	}
	return payloadStart, payloadEnd, true
}

func skipMPEG2Header(data []byte, payloadStart int, payloadEnd int) int {
	headerLen := int(data[payloadStart+2])
	start := payloadStart + 3 + headerLen
	if start > payloadEnd {
		return payloadEnd
	}
	return start
}

func skipMPEG1Header(data []byte, payloadStart int, payloadEnd int) int {
	p := payloadStart
	for p < payloadEnd && data[p] == 0xFF {
		p++
	}
	if p < payloadEnd && (data[p]&0xC0) == 0x40 {
		p += 2
	}
	if p < payloadEnd && (data[p]&0xF0) == 0x20 {
		if p+5 > payloadEnd {
			return payloadEnd
		}
		return p + 5
	}
	if p < payloadEnd && (data[p]&0xF0) == 0x30 {
		if p+10 > payloadEnd {
			return payloadEnd
		}
		return p + 10
	}
	if p < payloadEnd && data[p] == 0x0F {
		if p+1 > payloadEnd {
			return payloadEnd
		}
		return p + 1
	}
	return p
}

func resolvePayloadStart(data []byte, payloadStart int, payloadEnd int) int {
	if payloadStart >= payloadEnd {
		return payloadEnd
	}
	if (data[payloadStart] & 0xC0) == 0x80 {
		return skipMPEG2Header(data, payloadStart, payloadEnd)
	}
	return skipMPEG1Header(data, payloadStart, payloadEnd)
}

// extractSPUPacket merges private stream chunks belonging to DVD subtitle
// substreams (0x20..0x3F) and returns one complete SPU packet.
func extractSPUPacket(subData []byte, start int, end int) ([]byte, error) {
	return extractSPUPacketForSubstream(subData, start, end, 0)
}

// extractSPUPacketForSubstream merges private-stream subtitle chunks and returns
// one complete SPU packet. When targetSubstreamID is non-zero, only that DVD
// subtitle substream id (0x20..0x3F) is accepted; otherwise all subtitle
// substreams are accepted (legacy behavior used by low-level tests).
func extractSPUPacketForSubstream(subData []byte, start int, end int, targetSubstreamID byte) ([]byte, error) {
	if start < 0 {
		start = 0
	}
	if end > len(subData) {
		end = len(subData)
	}
	if end < start {
		end = start
	}

	// Merge subtitle substream chunks (0x20..0x3F) in one pass over the byte span.
	// This avoids the intermediate [][]byte + bytes.Join pattern and stops early
	// once the SPU packet's declared length is fully assembled.
	merged := make([]byte, 0, end-start)
	expectedSize := 0
	cursor := start

	for cursor+6 <= end {
		marker := findPESMarker(subData, cursor, end)
		if marker < 0 {
			break
		}

		pesPayloadStart, pesPayloadEnd, ok := getPESPayloadBounds(subData, marker, end)
		if !ok {
			cursor = marker + 4
			continue
		}

		payloadStart := resolvePayloadStart(subData, pesPayloadStart, pesPayloadEnd)
		if payloadStart >= pesPayloadEnd {
			cursor = marker + 4
			continue
		}

		payload := subData[payloadStart:pesPayloadEnd]
		if len(payload) == 0 {
			cursor = marker + 4
			continue
		}

		substreamID := payload[0]
		if substreamID >= 0x20 && substreamID <= 0x3F && len(payload) > 1 &&
			(targetSubstreamID == 0 || substreamID == targetSubstreamID) {
			merged = append(merged, payload[1:]...)
			if expectedSize == 0 && len(merged) >= 2 {
				expectedSize = int(merged[0])<<8 | int(merged[1])
			}
			if expectedSize > 0 && len(merged) >= expectedSize {
				return merged[:expectedSize], nil
			}
		}

		// Move to current PES end to avoid repeatedly scanning the same payload.
		cursor = pesPayloadEnd
	}

	if len(merged) < 2 {
		return nil, fmt.Errorf("%w at idx filepos", errNoSubtitlePayload)
	}

	if expectedSize > len(merged) {
		return nil, fmt.Errorf("incomplete subtitle payload between idx offsets")
	}

	return merged[:expectedSize], nil
}
