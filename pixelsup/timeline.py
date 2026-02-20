from __future__ import annotations

import json
from pathlib import Path

from .models import CuePlacement, SubtitleCue
from .utils import format_srt_timestamp


def write_srt(cues: list[SubtitleCue], placements: dict[int, CuePlacement], path: Path) -> None:
    lines: list[str] = []
    for ordinal, cue in enumerate(cues, start=1):
        placement = placements[cue.index]
        start = format_srt_timestamp(cue.start_ms)
        end = format_srt_timestamp(cue.end_ms)
        marker = f"[img:{placement.sheet_name}#{placement.position_in_sheet:02d}]"
        lines.append(str(ordinal))
        lines.append(f"{start} --> {end}")
        lines.append(marker)
        lines.append("")
    path.write_text("\n".join(lines), encoding="utf-8")


def write_mapping_json(cues: list[SubtitleCue], placements: dict[int, CuePlacement], path: Path) -> None:
    items: list[dict[str, object]] = []
    for cue in cues:
        placement = placements[cue.index]
        items.append(
            {
                "cue_index": cue.index,
                "start_ms": cue.start_ms,
                "end_ms": cue.end_ms,
                "sheet": placement.sheet_name,
                "position_in_sheet": placement.position_in_sheet,
            }
        )
    payload = {"items": items}
    path.write_text(json.dumps(payload, indent=2, ensure_ascii=False), encoding="utf-8")

