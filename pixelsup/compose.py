from __future__ import annotations

from collections.abc import Callable

from PIL import Image
from PIL import ImageDraw
from PIL import ImageFont

from .models import CuePlacement, PlacedSheet, SubtitleCue


def _load_index_font(size: int) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    for candidate in ("arialbd.ttf", "Arial Bold.ttf", "DejaVuSans-Bold.ttf", "arial.ttf"):
        try:
            return ImageFont.truetype(candidate, size=size)
        except OSError:
            continue
    return ImageFont.load_default()


def _fit_index_font(label: str, box_size: int) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    # Keep labels as large as possible, but always fully inside the square box.
    max_size = max(10, int(box_size * 0.9))
    probe = Image.new("RGB", (1, 1), (0, 0, 0))
    probe_draw = ImageDraw.Draw(probe)
    for size in range(max_size, 7, -1):
        font = _load_index_font(size)
        left, top, right, bottom = probe_draw.textbbox((0, 0), label, font=font)
        text_w = right - left
        text_h = bottom - top
        if text_w <= box_size - 2 and text_h <= box_size - 2:
            return font
    return _load_index_font(8)


def compose_sheets(
    cues: list[SubtitleCue],
    limit: int,
    padding: int,
    show_row_index: bool = True,
    progress_cb: Callable[[int, int], None] | None = None,
) -> tuple[list[PlacedSheet], dict[int, CuePlacement]]:
    if limit <= 0:
        raise ValueError("limit must be > 0")
    if padding < 0:
        raise ValueError("padding must be >= 0")

    sheets: list[PlacedSheet] = []
    placements: dict[int, CuePlacement] = {}
    total_sheets = (len(cues) + limit - 1) // limit if cues else 0

    for sheet_idx, start in enumerate(range(0, len(cues), limit), start=1):
        chunk = cues[start : start + limit]
        if not chunk:
            continue
        max_width = max(c.image.width for c in chunk)
        total_height = sum(c.image.height for c in chunk) + padding * (len(chunk) - 1)
        gutter_width = 0
        separator_width = padding if show_row_index and padding > 0 else 0
        font: ImageFont.FreeTypeFont | ImageFont.ImageFont | None = None
        index_box_size = 0
        if show_row_index:
            # Use a square index box based on the minimum cue height in this sheet.
            index_box_size = min(c.image.height for c in chunk)
            index_box_size = max(10, index_box_size)
            max_label = str(max(c.index for c in chunk))
            font = _fit_index_font(max_label, index_box_size)
            gutter_width = index_box_size

        canvas = Image.new("RGB", (gutter_width + separator_width + max_width, total_height), (0, 0, 0))
        draw = ImageDraw.Draw(canvas)
        subtitle_x_base = gutter_width + separator_width

        if separator_width > 0:
            draw.rectangle((gutter_width, 0, gutter_width + separator_width - 1, total_height - 1), fill=(255, 255, 255))

        y = 0
        for pos, cue in enumerate(chunk, start=1):
            x = subtitle_x_base + (max_width - cue.image.width) // 2
            canvas.paste(cue.image.convert("RGBA"), (x, y), cue.image.convert("RGBA"))
            if show_row_index and font is not None:
                label = str(cue.index)
                left, top, right, bottom = draw.textbbox((0, 0), label, font=font)
                text_w = right - left
                text_h = bottom - top
                box_y = y + max(0, (cue.image.height - index_box_size) // 2)
                tx = max(0, (gutter_width - text_w) // 2) - left
                ty = box_y + max(0, (index_box_size - text_h) // 2) - top
                draw.text((tx, ty), label, fill=(255, 255, 255), font=font)
            placements[cue.index] = CuePlacement(sheet_name=f"sheet_{sheet_idx:04d}.png", position_in_sheet=pos)
            y += cue.image.height
            if padding > 0 and pos < len(chunk):
                draw.rectangle((0, y, canvas.width - 1, y + padding - 1), fill=(255, 255, 255))
                y += padding

        sheets.append(PlacedSheet(name=f"sheet_{sheet_idx:04d}.png", image=canvas, cue_indexes=[c.index for c in chunk]))
        if progress_cb is not None:
            progress_cb(sheet_idx, total_sheets)
    return sheets, placements
