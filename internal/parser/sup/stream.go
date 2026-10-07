package sup

import "fmt"

var pgHeader = []byte{'P', 'G'}

// Segment represents one raw SUP segment packet.
// Parsing here is intentionally low-level: it only splits stream packets and
// exposes raw segment bodies. Higher-level PCS/PDS/ODS interpretation is done
// in later migration steps.
type Segment struct {
	PTS90K int
	Type   byte
	Body   []byte
}

// ParseSegments reads a raw SUP byte stream and splits it into packet segments.
//
// Packet structure:
//
//	0..1   : "PG" magic
//	2..5   : PTS (90kHz clock, big-endian)
//	6..9   : DTS (ignored here)
//	10     : segment type
//	11..12 : segment body length (big-endian)
//	13..   : body bytes
//
// The function returns an error on malformed headers or truncated segment bodies.
func ParseSegments(data []byte) ([]Segment, error) {
	segments := make([]Segment, 0, 64)
	i := 0
	for i+13 <= len(data) {
		if data[i] != pgHeader[0] || data[i+1] != pgHeader[1] {
			return nil, fmt.Errorf("invalid SUP header at offset %d", i)
		}

		pts := int(data[i+2])<<24 | int(data[i+3])<<16 | int(data[i+4])<<8 | int(data[i+5])
		segType := data[i+10]
		segSize := u16(data, i+11)

		bodyStart := i + 13
		bodyEnd := bodyStart + segSize
		if bodyEnd > len(data) {
			return nil, fmt.Errorf("segment overruns file at offset %d", i)
		}

		// Copy segment body to make Segment immutable against caller-side data reuse.
		body := make([]byte, segSize)
		copy(body, data[bodyStart:bodyEnd])

		segments = append(segments, Segment{
			PTS90K: pts,
			Type:   segType,
			Body:   body,
		})

		i = bodyEnd
	}
	// Any non-empty trailing bytes mean the stream ended in the middle of a packet.
	// We treat that as malformed input instead of silently ignoring corruption.
	if i != len(data) {
		return nil, fmt.Errorf("truncated SUP packet tail at offset %d", i)
	}
	return segments, nil
}
