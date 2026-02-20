from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from PIL import Image


@dataclass(slots=True)
class SubtitleCue:
    index: int
    start_ms: int
    end_ms: int
    image: Image.Image


@dataclass(slots=True)
class CuePlacement:
    sheet_name: str
    position_in_sheet: int


@dataclass(slots=True)
class PlacedSheet:
    name: str
    image: Image.Image
    cue_indexes: list[int]


@dataclass(slots=True)
class PgsPaletteEntry:
    y: int
    cr: int
    cb: int
    alpha: int


@dataclass(slots=True)
class PgsObjectDefinition:
    width: int
    height: int
    rle_data: bytes


@dataclass(slots=True)
class PgsCompositionObject:
    object_id: int
    x: int
    y: int
    crop: tuple[int, int, int, int] | None = None


@dataclass(slots=True)
class PgsDisplaySet:
    pts_90k: int | None
    palette_id: int | None
    objects: list[PgsCompositionObject]
    metadata: dict[str, Any]

