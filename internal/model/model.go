package model

import "image"

// SubtitleCue stores logical subtitle timing information independently of rendered image data.
type SubtitleCue struct {
	Index   int
	StartMS int
	EndMS   int
}

// RenderedCue couples timeline metadata with a decoded RGBA frame.
// The frame is kept separate from SubtitleCue so timeline-only writers can
// continue using SubtitleCue without image dependencies in their APIs.
type RenderedCue struct {
	Cue   SubtitleCue
	Frame *image.RGBA
}

// CuePlacement maps a cue index to one sprite sheet location.
type CuePlacement struct {
	SheetName       string
	PositionInSheet int
}
