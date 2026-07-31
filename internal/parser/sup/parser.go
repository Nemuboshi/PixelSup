package sup

import (
	"fmt"
	"image"
	"image/draw"
	"math"

	"pixelsup-go/internal/model"
)

const (
	segmentPDS = 0x14
	segmentODS = 0x15
	segmentPCS = 0x16
	segmentEND = 0x80
)

type paletteEntry struct {
	y     uint8
	cb    uint8
	cr    uint8
	alpha uint8
}

type objectDefinition struct {
	width   int
	height  int
	rleData []byte
}

type cropRect struct {
	x int
	y int
	w int
	h int
}

type compositionObject struct {
	objectID int
	x        int
	y        int
	crop     *cropRect
}

type displaySetState struct {
	hasPTS             bool
	pts90k             int
	paletteID          int
	compositionObjects []compositionObject
}

type objectAssembly struct {
	width          int
	height         int
	expectedRLELen int
	buf            []byte
}

type frameAtPTS struct {
	pts90k int
	frame  *image.RGBA
}

// ParseCuesAndFrames parses raw SUP bytes and returns fully rendered subtitle cues.
//
// Supported display-set behavior mirrors the Python reference pipeline:
//   - PCS updates the active display state (timing, palette id, object placements).
//   - PDS updates palette entries by palette id.
//   - ODS assembles RLE object payloads (including multi-segment objects).
//   - END finalizes one display set into one RGBA frame.
//
// Timing conversion follows PGS 90kHz ticks -> milliseconds with the same
// compatibility fallbacks used by the Python implementation.
func ParseCuesAndFrames(data []byte) ([]model.RenderedCue, error) {
	segments, err := ParseSegments(data)
	if err != nil {
		return nil, err
	}

	palettes := make(map[int]map[uint8]paletteEntry)
	objects := make(map[int]objectDefinition)
	assemblies := make(map[int]objectAssembly)
	state := displaySetState{}
	frames := make([]frameAtPTS, 0, len(segments)/4)

	for idx, seg := range segments {
		switch seg.Type {
		case segmentPCS:
			nextState, pcsErr := parsePCS(seg.Body, seg.PTS90K)
			if pcsErr != nil {
				return nil, fmt.Errorf("PCS parse failed at segment %d: %w", idx, pcsErr)
			}
			state = nextState

		case segmentPDS:
			if pdsErr := parsePDS(seg.Body, palettes); pdsErr != nil {
				return nil, fmt.Errorf("PDS parse failed at segment %d: %w", idx, pdsErr)
			}

		case segmentODS:
			if odsErr := parseODS(seg.Body, objects, assemblies); odsErr != nil {
				return nil, fmt.Errorf("ODS parse failed at segment %d: %w", idx, odsErr)
			}

		case segmentEND:
			if frame, ok := finalizeDisplaySet(state, palettes, objects); ok {
				frames = append(frames, frameAtPTS{pts90k: state.pts90k, frame: frame})
			}
			// END starts a fresh display set. Persistent palette/object stores are kept.
			state = displaySetState{}
		}
	}

	if len(frames) == 0 {
		return []model.RenderedCue{}, nil
	}

	rendered := make([]model.RenderedCue, 0, len(frames))
	for i := 0; i < len(frames); i++ {
		startMS := int(math.Round(float64(frames[i].pts90k) / 90.0))
		endMS := startMS + 2_000
		if i+1 < len(frames) {
			endMS = int(math.Round(float64(frames[i+1].pts90k) / 90.0))
		}
		if endMS <= startMS {
			// Keep cues visible when timestamps are duplicated or out-of-order.
			endMS = startMS + 500
		}

		rendered = append(rendered, model.RenderedCue{
			Cue: model.SubtitleCue{
				Index:   i + 1,
				StartMS: startMS,
				EndMS:   endMS,
			},
			Frame: frames[i].frame,
		})
	}

	return rendered, nil
}

func parsePCS(body []byte, pts90k int) (displaySetState, error) {
	if len(body) < 11 {
		return displaySetState{}, fmt.Errorf("PCS body too short: %d", len(body))
	}

	paletteID := int(body[9])
	count := int(body[10])
	objs := make([]compositionObject, 0, count)
	pos := 11

	for i := 0; i < count; i++ {
		if pos+8 > len(body) {
			break
		}

		obj := compositionObject{
			objectID: u16(body, pos),
			x:        u16(body, pos+4),
			y:        u16(body, pos+6),
		}
		cropFlag := body[pos+3]&0x40 != 0
		pos += 8

		if cropFlag {
			if pos+8 > len(body) {
				break
			}
			obj.crop = &cropRect{
				x: u16(body, pos),
				y: u16(body, pos+2),
				w: u16(body, pos+4),
				h: u16(body, pos+6),
			}
			pos += 8
		}

		objs = append(objs, obj)
	}

	return displaySetState{
		hasPTS:             true,
		pts90k:             pts90k,
		paletteID:          paletteID,
		compositionObjects: objs,
	}, nil
}

func parsePDS(body []byte, palettes map[int]map[uint8]paletteEntry) error {
	if len(body) < 2 {
		return fmt.Errorf("PDS body too short: %d", len(body))
	}

	paletteID := int(body[0])
	entries := make(map[uint8]paletteEntry)
	for pos := 2; pos+5 <= len(body); pos += 5 {
		idx := body[pos]
		entries[idx] = paletteEntry{
			y:     body[pos+1],
			cr:    body[pos+2],
			cb:    body[pos+3],
			alpha: body[pos+4],
		}
	}
	palettes[paletteID] = entries
	return nil
}

func parseODS(body []byte, objects map[int]objectDefinition, assemblies map[int]objectAssembly) error {
	if len(body) < 4 {
		return fmt.Errorf("ODS body too short: %d", len(body))
	}

	objID := u16(body, 0)
	sequenceFlag := body[3]
	first := sequenceFlag == 0x80 || sequenceFlag == 0xC0
	last := sequenceFlag == 0x40 || sequenceFlag == 0xC0
	pos := 4

	if first {
		if pos+7 > len(body) {
			return fmt.Errorf("ODS first-segment body too short: %d", len(body))
		}

		objectDataLength := u24(body, pos)
		width := u16(body, pos+3)
		height := u16(body, pos+5)
		pos += 7

		expectedRLELen := objectDataLength
		if objectDataLength >= 4 {
			// Some sources count width/height bytes in object_data_length; others do not.
			expectedRLELen = objectDataLength - 4
		}

		chunk := make([]byte, len(body[pos:]))
		copy(chunk, body[pos:])
		assemblies[objID] = objectAssembly{
			width:          width,
			height:         height,
			expectedRLELen: expectedRLELen,
			buf:            chunk,
		}

		if last {
			materializeObject(objID, objects, assemblies)
		}
		return nil
	}

	current, ok := assemblies[objID]
	if !ok {
		return nil
	}

	current.buf = append(current.buf, body[pos:]...)
	assemblies[objID] = current
	if last {
		materializeObject(objID, objects, assemblies)
	}

	return nil
}

func materializeObject(objID int, objects map[int]objectDefinition, assemblies map[int]objectAssembly) {
	assembled, ok := assemblies[objID]
	if !ok {
		return
	}

	rle := assembled.buf
	if len(rle) > assembled.expectedRLELen {
		rle = rle[:assembled.expectedRLELen]
	}
	stableCopy := make([]byte, len(rle))
	copy(stableCopy, rle)

	objects[objID] = objectDefinition{
		width:   assembled.width,
		height:  assembled.height,
		rleData: stableCopy,
	}
	delete(assemblies, objID)
}

func finalizeDisplaySet(
	state displaySetState,
	palettes map[int]map[uint8]paletteEntry,
	objects map[int]objectDefinition,
) (*image.RGBA, bool) {
	if !state.hasPTS || len(state.compositionObjects) == 0 {
		return nil, false
	}

	palette := palettes[state.paletteID]
	minX := state.compositionObjects[0].x
	minY := state.compositionObjects[0].y
	maxX := minX
	maxY := minY

	type renderedObject struct {
		x      int
		y      int
		sprite *image.RGBA
	}
	rendered := make([]renderedObject, 0, len(state.compositionObjects))

	for _, obj := range state.compositionObjects {
		if obj.x < minX {
			minX = obj.x
		}
		if obj.y < minY {
			minY = obj.y
		}

		defn, ok := objects[obj.objectID]
		if !ok {
			continue
		}

		sprite := renderObject(defn, palette)
		if obj.crop != nil {
			sprite = cropRGBA(sprite, *obj.crop)
		}

		rendered = append(rendered, renderedObject{x: obj.x, y: obj.y, sprite: sprite})

		if right := obj.x + sprite.Bounds().Dx(); right > maxX {
			maxX = right
		}
		if bottom := obj.y + sprite.Bounds().Dy(); bottom > maxY {
			maxY = bottom
		}
	}

	if len(rendered) == 0 {
		return nil, false
	}

	canvas := image.NewRGBA(image.Rect(0, 0, maxX-minX, maxY-minY))
	for _, item := range rendered {
		dst := image.Rect(
			item.x-minX,
			item.y-minY,
			item.x-minX+item.sprite.Bounds().Dx(),
			item.y-minY+item.sprite.Bounds().Dy(),
		)
		draw.Draw(canvas, dst, item.sprite, image.Point{}, draw.Over)
	}

	return canvas, true
}

func renderObject(defn objectDefinition, palette map[uint8]paletteEntry) *image.RGBA {
	indices := DecodeRLEIndices(defn.rleData, defn.width, defn.height)
	rgba := image.NewRGBA(image.Rect(0, 0, defn.width, defn.height))

	// Build a fixed-size index->RGBA lookup table once, then render by direct
	// slice indexing to avoid per-pixel map lookups and PixOffset math.
	var lut [256][4]byte
	for idx, entry := range palette {
		r, g, b, a := ycbcrToRGBA(int(entry.y), int(entry.cb), int(entry.cr), int(entry.alpha))
		lut[idx] = [4]byte{byte(r), byte(g), byte(b), byte(a)}
	}

	pix := rgba.Pix
	for i, idx := range indices {
		off := i * 4
		c := lut[idx]
		pix[off+0] = c[0]
		pix[off+1] = c[1]
		pix[off+2] = c[2]
		pix[off+3] = c[3]
	}

	return rgba
}

func cropRGBA(src *image.RGBA, crop cropRect) *image.RGBA {
	if crop.w <= 0 || crop.h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}

	dst := image.NewRGBA(image.Rect(0, 0, crop.w, crop.h))
	// draw.Draw handles out-of-bounds source areas by keeping destination pixels transparent.
	draw.Draw(dst, dst.Bounds(), src, image.Point{X: crop.x, Y: crop.y}, draw.Src)
	return dst
}
