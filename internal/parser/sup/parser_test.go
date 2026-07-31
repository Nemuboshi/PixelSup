package sup

import "testing"

func TestParseCuesAndFrames_BasicDisplaySetsAndTiming(t *testing.T) {
	pcs := func(pts int, objectID int, paletteID int, x int, y int) []byte {
		body := []byte{
			0x00, 0x00, // video width (unused here)
			0x00, 0x00, // video height (unused here)
			0x00,       // frame rate (unused)
			0x00, 0x00, // composition number
			0x00, // composition state
			0x00, // palette update flag
			byte(paletteID),
			0x01, // one composition object
		}
		body = append(body,
			byte(objectID>>8), byte(objectID),
			0x00, // window id
			0x00, // crop flag off
			byte(x>>8), byte(x),
			byte(y>>8), byte(y),
		)
		return packet(pts, 0x16, body)
	}

	pds := func(pts int, paletteID int, idx int, y int, cr int, cb int, alpha int) []byte {
		body := []byte{
			byte(paletteID),
			0x00, // palette version
			byte(idx), byte(y), byte(cr), byte(cb), byte(alpha),
		}
		return packet(pts, 0x14, body)
	}

	ods := func(pts int, objectID int, width int, height int, rle []byte) []byte {
		objDataLen := len(rle) + 4 // include width/height bytes for compatibility path
		body := []byte{
			byte(objectID >> 8), byte(objectID),
			0x00, // version
			0xC0, // first+last
			byte(objDataLen >> 16), byte(objDataLen >> 8), byte(objDataLen),
			byte(width >> 8), byte(width),
			byte(height >> 8), byte(height),
		}
		body = append(body, rle...)
		return packet(pts, 0x15, body)
	}

	end := func(pts int) []byte {
		return packet(pts, 0x80, nil)
	}

	data := make([]byte, 0, 256)
	data = append(data, pds(0, 0, 1, 235, 128, 128, 255)...)
	data = append(data, ods(0, 1, 1, 1, []byte{0x01})...)
	data = append(data, pcs(90_000, 1, 0, 10, 20)...)
	data = append(data, end(90_000)...)
	data = append(data, pcs(180_000, 1, 0, 10, 20)...)
	data = append(data, end(180_000)...)

	cues, err := ParseCuesAndFrames(data)
	if err != nil {
		t.Fatalf("ParseCuesAndFrames() error = %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("len(cues) = %d, want 2", len(cues))
	}

	if cues[0].Cue.StartMS != 1000 || cues[0].Cue.EndMS != 2000 {
		t.Fatalf("cue[0] timeline = %d..%d, want 1000..2000", cues[0].Cue.StartMS, cues[0].Cue.EndMS)
	}
	if cues[1].Cue.StartMS != 2000 || cues[1].Cue.EndMS != 4000 {
		t.Fatalf("cue[1] timeline = %d..%d, want 2000..4000", cues[1].Cue.StartMS, cues[1].Cue.EndMS)
	}

	if cues[0].Frame.Bounds().Dx() != 1 || cues[0].Frame.Bounds().Dy() != 1 {
		t.Fatalf("cue[0] frame size = %v, want 1x1", cues[0].Frame.Bounds())
	}
	r, g, b, a := cues[0].Frame.At(0, 0).RGBA()
	if r == 0 || g == 0 || b == 0 || a == 0 {
		t.Fatalf("cue[0] pixel is unexpectedly transparent/black: r=%d g=%d b=%d a=%d", r, g, b, a)
	}
}

func packet(pts int, segType byte, body []byte) []byte {
	out := make([]byte, 0, 13+len(body))
	out = append(out, 'P', 'G')
	out = append(out, byte(pts>>24), byte(pts>>16), byte(pts>>8), byte(pts))
	out = append(out, 0x00, 0x00, 0x00, 0x00) // dts ignored
	out = append(out, segType)
	out = append(out, byte(len(body)>>8), byte(len(body)))
	out = append(out, body...)
	return out
}
