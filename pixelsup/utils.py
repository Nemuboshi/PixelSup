from __future__ import annotations

from collections.abc import Iterable, Iterator


def format_srt_timestamp(ms: int) -> str:
    if ms < 0:
        ms = 0
    hours, rem = divmod(ms, 3_600_000)
    minutes, rem = divmod(rem, 60_000)
    seconds, millis = divmod(rem, 1_000)
    return f"{hours:02d}:{minutes:02d}:{seconds:02d},{millis:03d}"


def chunked(items: Iterable[int], size: int) -> Iterator[list[int]]:
    if size <= 0:
        raise ValueError("size must be > 0")
    buf: list[int] = []
    for item in items:
        buf.append(item)
        if len(buf) == size:
            yield buf
            buf = []
    if buf:
        yield buf

