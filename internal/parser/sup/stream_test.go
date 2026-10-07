package sup

import (
	"strings"
	"testing"
)

func mustPacket(pts int, segType byte, body []byte) []byte {
	packet := []byte{
		'P', 'G',
		byte(pts >> 24), byte(pts >> 16), byte(pts >> 8), byte(pts),
		0x00, 0x00, 0x00, 0x00, // dts is intentionally ignored by ParseSegments.
		segType,
		byte(len(body) >> 8), byte(len(body)),
	}
	packet = append(packet, body...)
	return packet
}

func TestParseSegments_InvalidHeader(t *testing.T) {
	data := mustPacket(1, 0x16, []byte{0x01})
	data[0] = 'X'
	_, err := ParseSegments(data)
	if err == nil {
		t.Fatalf("expected error for invalid header")
	}
	if !strings.Contains(err.Error(), "invalid SUP header") {
		t.Fatalf("error = %v, want invalid header error", err)
	}
}

func TestParseSegments_SinglePacket(t *testing.T) {
	// Packet format: 'PG' + pts(4) + dts(4) + segType(1) + segSize(2) + body(segSize)
	data := []byte{
		'P', 'G',
		0x00, 0x00, 0x00, 0x5A, // pts = 90
		0x00, 0x00, 0x00, 0x00, // dts (ignored)
		0x16,       // segment type
		0x00, 0x02, // size = 2
		0x11, 0x22, // body
	}

	segments, err := ParseSegments(data)
	if err != nil {
		t.Fatalf("ParseSegments() error = %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("segments len = %d, want 1", len(segments))
	}
	if segments[0].Type != 0x16 {
		t.Fatalf("segment type = %#x, want %#x", segments[0].Type, 0x16)
	}
	if segments[0].PTS90K != 90 {
		t.Fatalf("segment pts = %d, want 90", segments[0].PTS90K)
	}
	if len(segments[0].Body) != 2 || segments[0].Body[0] != 0x11 || segments[0].Body[1] != 0x22 {
		t.Fatalf("unexpected segment body: %#v", segments[0].Body)
	}
}

func TestParseSegments_OverrunBody(t *testing.T) {
	data := []byte{
		'P', 'G',
		0x00, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00,
		0x16,
		0x00, 0x03, // declares 3 bytes, but only 2 follow
		0x01, 0x02,
	}
	_, err := ParseSegments(data)
	if err == nil {
		t.Fatalf("expected overrun error")
	}
}

func TestParseSegments_TruncatedPacketTail(t *testing.T) {
	data := append(mustPacket(2, 0x14, nil), 0xFF)
	_, err := ParseSegments(data)
	if err == nil {
		t.Fatalf("expected truncated packet tail error")
	}
	if !strings.Contains(err.Error(), "truncated SUP packet tail") {
		t.Fatalf("error = %v, want truncated packet tail", err)
	}
}

func TestParseSegments_CopiesBody(t *testing.T) {
	data := mustPacket(3, 0x15, []byte{0xAA, 0xBB})
	segments, err := ParseSegments(data)
	if err != nil {
		t.Fatalf("ParseSegments() error = %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("segments len = %d, want 1", len(segments))
	}

	// Mutating caller-owned backing bytes must not change parsed segment bodies.
	data[len(data)-2] = 0x00
	data[len(data)-1] = 0x00
	if got := segments[0].Body; len(got) != 2 || got[0] != 0xAA || got[1] != 0xBB {
		t.Fatalf("segment body mutated through caller buffer: %#v", got)
	}
}
