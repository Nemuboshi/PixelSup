from __future__ import annotations

from dataclasses import dataclass
import base64
import json
from pathlib import Path
import time
import re
from collections.abc import Callable
from typing import Any

import httpx
import yaml

from .utils import format_srt_timestamp


@dataclass(slots=True)
class OCRConfig:
    api_base: str
    api_key: str
    model: str
    timeout_seconds: int = 120
    max_retries: int = 2
    retry_backoff_seconds: float = 1.0
    prompt_template: str | None = None


DEFAULT_JA_OCR_PROMPT = (
    "You are an OCR assistant. The image contains Japanese dialogue lines in a table.\n"
    "Transcribe all original text with high precision.\n"
    "There are exactly {expected_count} rows.\n"
    "Rules:\n"
    "1) Left side is row number, right side is content.\n"
    "2) Keep row order strictly aligned with row numbers, top to bottom.\n"
    "3) Do not include row numbers in output text.\n"
    "4) Keep each returned item as a single-line string. If original content wraps, use literal \\\\N to represent line breaks.\n"
    "   IMPORTANT: output must be valid JSON. Write escaped backslash as \\\\\\\\N in JSON strings (NOT raw \\N).\n"
    "5) Preserve punctuation exactly.\n"
    "6) If small ruby text exists, annotate it in full-width parentheses (...).\n"
    "7) Be strict about small kana vs normal kana.\n"
    "Output JSON only: {{\"lines\":[\"...\", \"...\"]}} with exactly {expected_count} items.\n"
    "Example valid JSON item: \"line_A\\\\\\\\Nline_B\""
)

def load_ocr_config(path: Path) -> OCRConfig:
    if not path.exists():
        raise ValueError(f"Config file not found: {path}")
    raw = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    ocr = raw.get("ocr", {})
    api_base = str(ocr.get("api_base", "")).strip()
    api_key = str(ocr.get("api_key", "")).strip()
    model = str(ocr.get("model", "")).strip()
    timeout_seconds = int(ocr.get("timeout_seconds", 120))
    max_retries = int(ocr.get("max_retries", 2))
    retry_backoff_seconds = float(ocr.get("retry_backoff_seconds", 1.0))
    prompt_template = ocr.get("prompt_template")
    if not api_base or not api_key or not model:
        raise ValueError("ocr_config.yaml missing required fields: ocr.api_base, ocr.api_key, ocr.model")
    if max_retries < 0:
        raise ValueError("ocr.max_retries must be >= 0")
    if retry_backoff_seconds < 0:
        raise ValueError("ocr.retry_backoff_seconds must be >= 0")
    return OCRConfig(
        api_base=api_base.rstrip("/"),
        api_key=api_key,
        model=model,
        timeout_seconds=timeout_seconds,
        max_retries=max_retries,
        retry_backoff_seconds=retry_backoff_seconds,
        prompt_template=str(prompt_template) if prompt_template is not None else None,
    )


def _data_uri(path: Path) -> str:
    raw = path.read_bytes()
    b64 = base64.b64encode(raw).decode("ascii")
    return f"data:image/png;base64,{b64}"


def _extract_text_content(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, dict) and "text" in item:
                parts.append(str(item["text"]))
        return "\n".join(parts)
    return str(content)


def _align_line_count(lines: list[str], expected_count: int) -> list[str]:
    out = [str(x).strip() for x in lines]
    if len(out) < expected_count:
        out.extend([""] * (expected_count - len(out)))
    if len(out) > expected_count:
        if expected_count == 0:
            out = []
        elif expected_count == 1:
            out = [" ".join(out)]
        else:
            out = out[: expected_count - 1] + [" ".join(out[expected_count - 1 :])]
    return out


def _parse_lines_from_response(content_text: str, expected_count: int) -> list[str]:
    text = content_text.strip()
    if text.startswith("```"):
        text = text.strip("`")
        if text.startswith("json"):
            text = text[4:].strip()
    try:
        data = json.loads(text)
        lines = data.get("lines", [])
        if not isinstance(lines, list):
            lines = []
        return _align_line_count([str(x).strip() for x in lines], expected_count)
    except json.JSONDecodeError:
        # Some providers occasionally truncate JSON in the middle of "lines".
        # Fallback: extract JSON string literals after the "lines" key and align count.
        idx = text.find('"lines"')
        scan = text[idx:] if idx >= 0 else text
        raw_items = re.findall(r'"((?:\\.|[^"\\])*)"', scan)
        lines: list[str] = []
        for raw in raw_items:
            try:
                lines.append(json.loads(f'"{raw}"').strip())
            except Exception:
                continue
        # Drop the "lines" key token itself if matched as a string literal.
        if lines and lines[0] == "lines":
            lines = lines[1:]
        if not lines:
            raise
        return _align_line_count(lines, expected_count)


def _build_prompt(config: OCRConfig, expected_count: int) -> str:
    template = config.prompt_template or DEFAULT_JA_OCR_PROMPT
    try:
        return template.format(expected_count=expected_count)
    except Exception as exc:  # noqa: BLE001
        raise ValueError("Invalid ocr.prompt_template. It must support {expected_count}.") from exc


def ocr_sheet_lines(config: OCRConfig, image_path: Path, expected_count: int) -> list[str]:
    prompt = _build_prompt(config, expected_count)
    payload = {
        "model": config.model,
        "temperature": 0,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {"type": "image_url", "image_url": {"url": _data_uri(image_path)}},
                ],
            }
        ],
        "response_format": {"type": "json_object"},
    }
    headers = {"Authorization": f"Bearer {config.api_key}", "Content-Type": "application/json"}
    url = f"{config.api_base}/chat/completions"
    with httpx.Client(timeout=config.timeout_seconds) as client:
        resp = client.post(url, headers=headers, json=payload)
        if resp.status_code >= 400:
            detail = resp.text.strip()
            raise RuntimeError(f"OCR API request failed ({resp.status_code}) at {url}: {detail[:500]}")
        data = resp.json()
    content = data["choices"][0]["message"]["content"]
    content_text = _extract_text_content(content)
    return _parse_lines_from_response(content_text, expected_count)


def _ocr_sheet_lines_with_retry(config: OCRConfig, image_path: Path, expected_count: int) -> list[str]:
    attempts = config.max_retries + 1
    last_error: Exception | None = None
    for attempt in range(attempts):
        try:
            return ocr_sheet_lines(config, image_path, expected_count)
        except Exception as exc:  # noqa: BLE001 - keep retries broad for transient OCR failures.
            last_error = exc
            if attempt >= attempts - 1:
                break
            if config.retry_backoff_seconds > 0:
                time.sleep(config.retry_backoff_seconds * (2**attempt))
    raise RuntimeError(f"OCR failed for sheet {image_path.name} after {attempts} attempts: {last_error}") from last_error


def run_ocr_on_output(
    output_dir: Path,
    config_path: Path,
    overwrite_timeline: bool = True,
    progress_cb: Callable[[int, int, str], None] | None = None,
) -> Path:
    config = load_ocr_config(config_path)
    map_path = output_dir / "mapping.json"
    if not map_path.exists():
        raise ValueError(f"mapping.json not found in {output_dir}")
    data = json.loads(map_path.read_text(encoding="utf-8"))
    items = data.get("items", [])
    if not isinstance(items, list) or not items:
        raise ValueError("mapping.json contains no items")

    by_sheet: dict[str, list[dict[str, Any]]] = {}
    for item in items:
        sheet = str(item["sheet"])
        by_sheet.setdefault(sheet, []).append(item)
    for sheet_items in by_sheet.values():
        sheet_items.sort(key=lambda x: int(x["position_in_sheet"]))

    cue_texts: dict[int, str] = {}
    ordered_sheets = sorted(by_sheet.items())
    total_sheets = len(ordered_sheets)
    for done, (sheet_name, sheet_items) in enumerate(ordered_sheets, start=1):
        sheet_path = output_dir / sheet_name
        if not sheet_path.exists():
            raise ValueError(f"Sheet image missing: {sheet_path}")
        lines = _ocr_sheet_lines_with_retry(config, sheet_path, expected_count=len(sheet_items))
        for item, line in zip(sheet_items, lines):
            cue_texts[int(item["cue_index"])] = str(line).strip()
        if progress_cb is not None:
            progress_cb(done, total_sheets, sheet_name)

    sorted_items = sorted(items, key=lambda x: int(x["cue_index"]))
    srt_lines: list[str] = []
    for i, item in enumerate(sorted_items, start=1):
        start = format_srt_timestamp(int(item["start_ms"]))
        end = format_srt_timestamp(int(item["end_ms"]))
        cue_index = int(item["cue_index"])
        text = cue_texts.get(cue_index, "").strip() or f"[img:{item['sheet']}#{int(item['position_in_sheet']):02d}]"
        srt_lines.append(str(i))
        srt_lines.append(f"{start} --> {end}")
        srt_lines.append(text)
        srt_lines.append("")

    out_path = output_dir / ("timeline.srt" if overwrite_timeline else "timeline.ocr.srt")
    out_path.write_text("\n".join(srt_lines), encoding="utf-8")
    return out_path

