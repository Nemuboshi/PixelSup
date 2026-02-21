from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from PIL import Image

from .models import (
    PgsCompositionObject,
    PgsObjectDefinition,
    PgsPaletteEntry,
    SubtitleCue,
)

PG_HEADER = b"PG"
SEGMENT_PCS = 0x16
SEGMENT_WDS = 0x17
SEGMENT_PDS = 0x14
SEGMENT_ODS = 0x15
SEGMENT_END = 0x80


@dataclass(slots=True)
class _DisplaySetState:
    # One display set usually corresponds to one rendered subtitle event.
    pts_90k: int | None = None
    palette_id: int | None = None
    composition_objects: list[PgsCompositionObject] | None = None


def _u16(data: bytes, off: int) -> int:
    return (data[off] << 8) | data[off + 1]


def _u24(data: bytes, off: int) -> int:
    return (data[off] << 16) | (data[off + 1] << 8) | data[off + 2]


def _ycbcr_to_rgba(y: int, cb: int, cr: int, alpha: int) -> tuple[int, int, int, int]:
    c = y - 16
    d = cb - 128
    e = cr - 128
    r = (298 * c + 409 * e + 128) >> 8
    g = (298 * c - 100 * d - 208 * e + 128) >> 8
    b = (298 * c + 516 * d + 128) >> 8
    r = min(255, max(0, r))
    g = min(255, max(0, g))
    b = min(255, max(0, b))
    return (r, g, b, alpha)


def _decode_rle_indices(data: bytes, width: int, height: int) -> list[int]:
    """
    Decode PGS RLE payload into palette indexes (row-major).

    PGS uses control sequences where `00 00` is explicit end-of-line.
    This decoder follows explicit line breaks and pads incomplete rows
    with transparent index 0 to keep width/height stable.
    """
    out: list[int] = []
    x = 0
    y = 0
    i = 0

    def append_pixel(color: int) -> None:
        nonlocal x, y
        if y >= height:
            return
        if x >= width:
            # Line breaks are explicit (00 00). Ignore overflow until next EOL.
            return
        out.append(color)
        x += 1

    def flush_line() -> None:
        nonlocal x, y
        if y >= height:
            return
        if x < width:
            out.extend([0] * (width - x))
        x = 0
        y += 1

    while i < len(data) and y < height:
        b = data[i]
        i += 1
        if b != 0:
            append_pixel(b)
            continue

        if i >= len(data):
            break
        b2 = data[i]
        i += 1
        if b2 == 0:
            flush_line()
            continue

        if b2 < 0x40:
            run = b2
            color = 0
        elif b2 < 0x80:
            if i >= len(data):
                break
            run = ((b2 - 0x40) << 8) + data[i]
            i += 1
            color = 0
        elif b2 < 0xC0:
            if i + 1 > len(data):
                break
            run = b2 - 0x80
            color = data[i]
            i += 1
        else:
            if i + 2 > len(data):
                break
            run = ((b2 - 0xC0) << 8) + data[i]
            color = data[i + 1]
            i += 2

        for _ in range(run):
            if y >= height:
                break
            append_pixel(color)

    # Flush final partially filled line.
    if y < height and x > 0:
        flush_line()

    if len(out) < width * height:
        out.extend([0] * (width * height - len(out)))
    return out[: width * height]


def _render_object(defn: PgsObjectDefinition, palette: dict[int, PgsPaletteEntry]) -> Image.Image:
    # Convert palette indexes into RGBA pixels through YCbCr->RGB conversion.
    indices = _decode_rle_indices(defn.rle_data, defn.width, defn.height)
    rgba = Image.new("RGBA", (defn.width, defn.height), (0, 0, 0, 0))
    pixels: list[tuple[int, int, int, int]] = []
    for y in range(defn.height):
        row = y * defn.width
        for x in range(defn.width):
            idx = indices[row + x]
            entry = palette.get(idx)
            if entry is None:
                pixels.append((0, 0, 0, 0))
            else:
                pixels.append(_ycbcr_to_rgba(entry.y, entry.cb, entry.cr, entry.alpha))
    rgba.putdata(pixels)
    return rgba


def _finalize_display_set(
    state: _DisplaySetState,
    palettes: dict[int, dict[int, PgsPaletteEntry]],
    objects: dict[int, PgsObjectDefinition],
) -> tuple[int, Image.Image] | None:
    # Compose all subtitle objects in one display set into a single minimal canvas.
    if state.pts_90k is None or state.composition_objects is None:
        return None
    if not state.composition_objects:
        return None

    palette = palettes.get(state.palette_id or 0, {})
    # Render onto a minimal bounding canvas that encloses all composition objects.
    min_x = min(obj.x for obj in state.composition_objects)
    min_y = min(obj.y for obj in state.composition_objects)
    max_x = min_x
    max_y = min_y
    rendered: list[tuple[PgsCompositionObject, Image.Image]] = []
    for obj in state.composition_objects:
        defn = objects.get(obj.object_id)
        if defn is None:
            continue
        sprite = _render_object(defn, palette)
        if obj.crop is not None:
            cx, cy, cw, ch = obj.crop
            sprite = sprite.crop((cx, cy, cx + cw, cy + ch))
        rendered.append((obj, sprite))
        max_x = max(max_x, obj.x + sprite.width)
        max_y = max(max_y, obj.y + sprite.height)
    if not rendered:
        return None

    canvas = Image.new("RGBA", (max_x - min_x, max_y - min_y), (0, 0, 0, 0))
    for obj, sprite in rendered:
        x = obj.x - min_x
        y = obj.y - min_y
        canvas.paste(sprite, (x, y), sprite)

    return state.pts_90k, canvas


def parse_sup(path: Path, progress_cb: Callable[[int, int], None] | None = None) -> list[SubtitleCue]:
    """
    Parse a Blu-ray SUP/PGS stream and return decoded subtitle cue images.

    Minimal supported pipeline:
    - PCS: composition timing + object placement
    - PDS: palette entries
    - ODS: run-length encoded object bitmap
    - END: finalize one display set into one cue frame
    """
    data = path.read_bytes()
    total_bytes = len(data)
    i = 0
    palettes: dict[int, dict[int, PgsPaletteEntry]] = {}
    objects: dict[int, PgsObjectDefinition] = {}
    object_assembly: dict[int, tuple[int, int, int, bytearray]] = {}
    display_state = _DisplaySetState()
    frames: list[tuple[int, Image.Image]] = []

    if progress_cb is not None:
        progress_cb(0, total_bytes)

    # Stream is a sequence of PG packet headers + segment bodies.
    while i + 13 <= len(data):
        if data[i : i + 2] != PG_HEADER:
            raise ValueError(f"Invalid SUP header at offset {i}")
        pts_90k = int.from_bytes(data[i + 2 : i + 6], "big")
        # DTS at i+6:i+10 is ignored
        seg_type = data[i + 10]
        seg_size = int.from_bytes(data[i + 11 : i + 13], "big")
        body_start = i + 13
        body_end = body_start + seg_size
        if body_end > len(data):
            raise ValueError(f"Segment overruns file at offset {i}")
        body = data[body_start:body_end]

        if seg_type == SEGMENT_PCS:
            if len(body) < 11:
                raise ValueError(f"PCS too short at offset {i}")
            palette_id = body[9]
            comp_obj_count = body[10]
            pos = 11
            comp_objs: list[PgsCompositionObject] = []
            for _ in range(comp_obj_count):
                if pos + 8 > len(body):
                    break
                object_id = _u16(body, pos)
                # window id at pos+2 ignored
                crop_flag = body[pos + 3] & 0x40
                x = _u16(body, pos + 4)
                y = _u16(body, pos + 6)
                pos += 8
                crop = None
                if crop_flag:
                    if pos + 8 > len(body):
                        break
                    crop_x = _u16(body, pos)
                    crop_y = _u16(body, pos + 2)
                    crop_w = _u16(body, pos + 4)
                    crop_h = _u16(body, pos + 6)
                    crop = (crop_x, crop_y, crop_w, crop_h)
                    pos += 8
                comp_objs.append(PgsCompositionObject(object_id=object_id, x=x, y=y, crop=crop))
            display_state = _DisplaySetState(pts_90k=pts_90k, palette_id=palette_id, composition_objects=comp_objs)

        elif seg_type == SEGMENT_PDS:
            if len(body) < 2:
                raise ValueError(f"PDS too short at offset {i}")
            palette_id = body[0]
            entries: dict[int, PgsPaletteEntry] = {}
            pos = 2
            while pos + 5 <= len(body):
                idx = body[pos]
                y = body[pos + 1]
                cr = body[pos + 2]
                cb = body[pos + 3]
                alpha = body[pos + 4]
                entries[idx] = PgsPaletteEntry(y=y, cr=cr, cb=cb, alpha=alpha)
                pos += 5
            palettes[palette_id] = entries

        elif seg_type == SEGMENT_ODS:
            if len(body) < 4:
                raise ValueError(f"ODS too short at offset {i}")
            obj_id = _u16(body, 0)
            sequence_flag = body[3]
            pos = 4
            first_in_seq = sequence_flag in (0x80, 0xC0)
            last_in_seq = sequence_flag in (0x40, 0xC0)

            if first_in_seq:
                if pos + 7 > len(body):
                    raise ValueError(f"ODS(first) too short at offset {i}")
                object_data_length = _u24(body, pos)
                width = _u16(body, pos + 3)
                height = _u16(body, pos + 5)
                pos += 7
                chunk = bytearray(body[pos:])
                # Some streams encode object_data_length including width/height bytes,
                # some use only raw RLE length. Keep compatibility with both.
                expected_rle_len = object_data_length
                if object_data_length >= 4:
                    expected_rle_len = object_data_length - 4
                object_assembly[obj_id] = (width, height, expected_rle_len, chunk)
                if last_in_seq:
                    objects[obj_id] = PgsObjectDefinition(width=width, height=height, rle_data=bytes(chunk[:expected_rle_len]))
                    object_assembly.pop(obj_id, None)
            else:
                current = object_assembly.get(obj_id)
                if current is not None:
                    width, height, expected_rle_len, buf = current
                    buf.extend(body[pos:])
                    object_assembly[obj_id] = (width, height, expected_rle_len, buf)
                    if last_in_seq:
                        objects[obj_id] = PgsObjectDefinition(width=width, height=height, rle_data=bytes(buf[:expected_rle_len]))
                        object_assembly.pop(obj_id, None)

        elif seg_type == SEGMENT_WDS:
            # Window definition is not required for our minimal sprite extraction path.
            pass

        elif seg_type == SEGMENT_END:
            # END marks a display set boundary; render a cue if enough data exists.
            finalized = _finalize_display_set(display_state, palettes, objects)
            if finalized is not None:
                frames.append(finalized)
            display_state = _DisplaySetState()

        i = body_end
        if progress_cb is not None:
            progress_cb(i, total_bytes)

    if not frames:
        return []

    # Convert PTS ticks (90kHz clock) to millisecond timeline.
    cues: list[SubtitleCue] = []
    for idx, (start_pts, image) in enumerate(frames, start=1):
        start_ms = int(round(start_pts / 90))
        if idx < len(frames):
            next_pts = frames[idx][0]
            end_ms = int(round(next_pts / 90))
        else:
            end_ms = start_ms + 2_000
        if end_ms <= start_ms:
            end_ms = start_ms + 500
        cues.append(SubtitleCue(index=idx, start_ms=start_ms, end_ms=end_ms, image=image))
    return cues
