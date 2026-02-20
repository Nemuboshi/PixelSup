from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
import re

from PIL import Image

from .models import SubtitleCue

_TIMESTAMP_RE = re.compile(r"(\d{2}):(\d{2}):(\d{2}):(\d{3})")
_FILEPOS_RE = re.compile(r"filepos:\s*([0-9A-Fa-f]+)")


@dataclass(slots=True)
class _IdxEntry:
    start_ms: int
    filepos: int


@dataclass(slots=True)
class _SpuControl:
    colormap: list[int]
    alpha: list[int]
    x1: int
    x2: int
    y1: int
    y2: int
    offset1: int
    offset2: int


def _parse_idx_timestamp(raw: str) -> int:
    match = _TIMESTAMP_RE.fullmatch(raw.strip())
    if not match:
        raise ValueError(f"Invalid idx timestamp: {raw}")
    hh, mm, ss, ms = (int(x) for x in match.groups())
    return hh * 3_600_000 + mm * 60_000 + ss * 1_000 + ms


def _parse_idx(path: Path) -> tuple[list[int], list[_IdxEntry]]:
    text = path.read_text(encoding="cp1252", errors="replace")
    palette = [0x000000] * 16
    entries: list[_IdxEntry] = []

    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        lower = line.lower()

        if lower.startswith("palette:"):
            payload = line.split(":", 1)[1].strip()
            cols = [item.strip() for item in payload.split(",") if item.strip()]
            for i, token in enumerate(cols[:16]):
                palette[i] = int(token, 16)
            continue

        if lower.startswith("timestamp:"):
            ts_match = _TIMESTAMP_RE.search(line)
            fp_match = _FILEPOS_RE.search(line)
            if not ts_match or not fp_match:
                continue
            start_ms = _parse_idx_timestamp(ts_match.group(0))
            filepos = int(fp_match.group(1), 16)
            entries.append(_IdxEntry(start_ms=start_ms, filepos=filepos))

    entries.sort(key=lambda e: e.filepos)
    return palette, entries


def _get_nibble(data: bytes, nibble_idx: int) -> int:
    byte = data[nibble_idx // 2] if (nibble_idx // 2) < len(data) else 0
    if nibble_idx & 1:
        return byte & 0x0F
    return byte >> 4


def _read_run_code(packet: bytes, nibble_idx: int, x: int, width: int) -> tuple[int, int]:
    v = _get_nibble(packet, nibble_idx)
    nibble_idx += 1
    if v < 0x4:
        v = (v << 4) | _get_nibble(packet, nibble_idx)
        nibble_idx += 1
    if v < 0x10:
        v = (v << 4) | _get_nibble(packet, nibble_idx)
        nibble_idx += 1
    if v < 0x40:
        v = (v << 4) | _get_nibble(packet, nibble_idx)
        nibble_idx += 1
    if v < 0x4:
        v |= (width - x) << 2
    return v, nibble_idx


def _decode_rle_field(
    bitmap: list[int],
    width: int,
    total_height: int,
    packet: bytes,
    offset_bytes: int,
    first_row: int,
    row_count: int,
) -> None:
    # Match FFmpeg's DVD subtitle nibble RLE decode behavior.
    nibble_idx = offset_bytes * 2
    nibble_end = len(packet) * 2
    for row in range(row_count):
        y = first_row + row * 2
        if y >= total_height:
            break
        x = 0
        while x < width and nibble_idx < nibble_end:
            v, nibble_idx = _read_run_code(packet, nibble_idx, x, width)
            run = v >> 2
            color = v & 0x03
            if run <= 0:
                run = width - x
            run = min(run, width - x)
            row_off = y * width
            for i in range(run):
                bitmap[row_off + x + i] = color
            x += run
        if nibble_idx & 1:
            nibble_idx += 1


def _try_apply_command(cmd: int, packet: bytes, pos: int, control: _SpuControl) -> tuple[int, bool]:
    if cmd in (0x00, 0x01, 0x02):
        return pos, True

    if cmd == 0x03 and pos + 2 <= len(packet):
        b0, b1 = packet[pos], packet[pos + 1]
        control.colormap[3] = b0 >> 4
        control.colormap[2] = b0 & 0x0F
        control.colormap[1] = b1 >> 4
        control.colormap[0] = b1 & 0x0F
        return pos + 2, True

    if cmd == 0x04 and pos + 2 <= len(packet):
        b0, b1 = packet[pos], packet[pos + 1]
        control.alpha[3] = b0 >> 4
        control.alpha[2] = b0 & 0x0F
        control.alpha[1] = b1 >> 4
        control.alpha[0] = b1 & 0x0F
        return pos + 2, True

    if cmd == 0x05 and pos + 6 <= len(packet):
        b0, b1, b2, b3, b4, b5 = packet[pos : pos + 6]
        control.x1 = (b0 << 4) | (b1 >> 4)
        control.x2 = ((b1 & 0x0F) << 8) | b2
        control.y1 = (b3 << 4) | (b4 >> 4)
        control.y2 = ((b4 & 0x0F) << 8) | b5
        return pos + 6, True

    if cmd == 0x06 and pos + 4 <= len(packet):
        control.offset1 = (packet[pos] << 8) | packet[pos + 1]
        control.offset2 = (packet[pos + 2] << 8) | packet[pos + 3]
        return pos + 4, True

    return pos, False


def _decode_vobsub_spu(packet: bytes, palette: list[int]) -> Image.Image:
    if len(packet) < 4:
        raise ValueError("SPU packet too short")
    packet_size = (packet[0] << 8) | packet[1]
    ctrl_offset = (packet[2] << 8) | packet[3]
    if packet_size > len(packet):
        raise ValueError("Incomplete SPU packet")
    packet = packet[:packet_size]

    control = _SpuControl(
        colormap=[0, 1, 2, 3],
        alpha=[0xF, 0xF, 0xF, 0xF],
        x1=0,
        x2=0,
        y1=0,
        y2=0,
        offset1=0,
        offset2=0,
    )

    cmd_pos = ctrl_offset
    while 0 < cmd_pos < len(packet):
        if cmd_pos + 4 > len(packet):
            break
        next_cmd = (packet[cmd_pos + 2] << 8) | packet[cmd_pos + 3]
        pos = cmd_pos + 4

        while pos < len(packet):
            cmd = packet[pos]
            pos += 1
            if cmd == 0xFF:
                break
            pos, handled = _try_apply_command(cmd, packet, pos, control)
            if not handled:
                break

        if next_cmd <= cmd_pos or next_cmd >= len(packet):
            break
        cmd_pos = next_cmd

    width = control.x2 - control.x1 + 1
    # Keep parity with FFmpeg dvdsub decoder behavior.
    height = control.y2 - control.y1
    if width <= 0 or height <= 0:
        raise ValueError("Invalid VobSub dimensions")
    if control.offset1 <= 0 or control.offset2 <= 0:
        raise ValueError("Missing VobSub bitmap offsets")

    bitmap = [0] * (width * height)
    _decode_rle_field(bitmap, width, height, packet, control.offset1, first_row=0, row_count=(height + 1) // 2)
    _decode_rle_field(bitmap, width, height, packet, control.offset2, first_row=1, row_count=height // 2)

    rgba: list[tuple[int, int, int, int]] = []
    for pix in bitmap:
        pal_idx = control.colormap[pix] & 0x0F
        rgb = palette[pal_idx] if pal_idx < len(palette) else 0xFFFFFF
        r = (rgb >> 16) & 0xFF
        g = (rgb >> 8) & 0xFF
        b = rgb & 0xFF
        a = (control.alpha[pix] & 0x0F) * 17
        rgba.append((r, g, b, a))

    image = Image.new("RGBA", (width, height))
    image.putdata(rgba)
    return image


def _find_pes_marker(data: bytes, cursor: int, end: int) -> int:
    return data.find(b"\x00\x00\x01\xbd", cursor, end)


def _get_pes_payload_bounds(data: bytes, marker: int, end: int) -> tuple[int, int] | None:
    if marker + 6 > end:
        return None
    pes_len = (data[marker + 4] << 8) | data[marker + 5]
    payload_end = marker + 6 + pes_len if pes_len else end
    payload_end = min(payload_end, end)
    payload_start = marker + 6
    if payload_start + 3 > payload_end:
        return None
    return payload_start, payload_end


def _skip_mpeg2_header(data: bytes, payload_start: int, payload_end: int) -> int:
    header_len = data[payload_start + 2]
    return min(payload_start + 3 + header_len, payload_end)


def _skip_mpeg1_header(data: bytes, payload_start: int, payload_end: int) -> int:
    p = payload_start
    while p < payload_end and data[p] == 0xFF:
        p += 1
    if p < payload_end and (data[p] & 0xC0) == 0x40:
        p += 2
    if p < payload_end and (data[p] & 0xF0) == 0x20:
        return min(p + 5, payload_end)
    if p < payload_end and (data[p] & 0xF0) == 0x30:
        return min(p + 10, payload_end)
    if p < payload_end and data[p] == 0x0F:
        return min(p + 1, payload_end)
    return p


def _resolve_payload_start(data: bytes, payload_start: int, payload_end: int) -> int:
    if (data[payload_start] & 0xC0) == 0x80:
        return _skip_mpeg2_header(data, payload_start, payload_end)
    return _skip_mpeg1_header(data, payload_start, payload_end)


def _find_private_stream_packets(data: bytes, start: int, end: int) -> list[bytes]:
    packets: list[bytes] = []
    cursor = start
    while cursor + 6 <= end:
        marker = _find_pes_marker(data, cursor, end)
        if marker < 0:
            break
        bounds = _get_pes_payload_bounds(data, marker, end)
        if bounds is None:
            cursor = marker + 4
            continue
        pes_payload_start, pes_payload_end = bounds
        payload_start = _resolve_payload_start(data, pes_payload_start, pes_payload_end)
        if payload_start >= pes_payload_end:
            cursor = marker + 4
            continue

        payload = data[payload_start:pes_payload_end]
        if payload:
            packets.append(payload)
        cursor = marker + 4
    return packets


def _extract_spu_packet(sub_data: bytes, start: int, end: int) -> bytes:
    payloads = _find_private_stream_packets(sub_data, start, end)
    chunks: list[bytes] = []
    for payload in payloads:
        substream_id = payload[0]
        if 0x20 <= substream_id <= 0x3F:
            chunks.append(payload[1:])
    merged = b"".join(chunks)
    if len(merged) < 2:
        raise ValueError("No subtitle payload found at idx filepos")
    expected_size = (merged[0] << 8) | merged[1]
    if expected_size > len(merged):
        raise ValueError("Incomplete subtitle payload between idx offsets")
    return merged[:expected_size]


def parse_idx_sub(path: Path, progress_cb: Callable[[int, int], None] | None = None) -> list[SubtitleCue]:
    if path.suffix.lower() != ".idx":
        raise ValueError("Input must be an .idx file")
    sub_path = path.with_suffix(".sub")
    if not sub_path.exists():
        raise ValueError(f"Matching .sub file not found: {sub_path}")

    palette, entries = _parse_idx(path)
    if not entries:
        return []

    sub_data = sub_path.read_bytes()
    total = len(entries)
    cues: list[SubtitleCue] = []
    for i, entry in enumerate(entries):
        start = entry.filepos
        end = entries[i + 1].filepos if i + 1 < total else len(sub_data)
        packet = _extract_spu_packet(sub_data, start, end)
        image = _decode_vobsub_spu(packet, palette)
        end_ms = entries[i + 1].start_ms if i + 1 < total else entry.start_ms + 2_000
        if end_ms <= entry.start_ms:
            end_ms = entry.start_ms + 500
        cues.append(SubtitleCue(index=i + 1, start_ms=entry.start_ms, end_ms=end_ms, image=image))
        if progress_cb is not None:
            progress_cb(i + 1, total)
    return cues
