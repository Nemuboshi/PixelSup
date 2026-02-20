import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from PIL import Image

from pixelsup.ocr import OCRConfig, _build_prompt, _parse_lines_from_response, run_ocr_on_output


class OCRTest(unittest.TestCase):
    def test_default_prompt_requires_no_row_numbers_in_output(self) -> None:
        prompt = _build_prompt(
            OCRConfig(api_base="https://x/v1", api_key="k", model="m"),
            expected_count=3,
        )
        self.assertIn("Do not include row numbers in output text.", prompt)
        self.assertIn("exactly 3 rows", prompt)
        self.assertIn("NOT raw \\N", prompt)

    def test_parse_lines_preserves_newlines_inside_item(self) -> None:
        parsed = _parse_lines_from_response('{"lines":["hello\\nworld","a\\rb"]}', expected_count=2)
        self.assertEqual(parsed, ["hello\nworld", "a\rb"])

    def test_parse_lines_overflow_merges_tail(self) -> None:
        parsed = _parse_lines_from_response('{"lines":["a","b","c"]}', expected_count=2)
        self.assertEqual(parsed, ["a", "b c"])

    def test_run_ocr_on_output_backfills_timeline(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            # Minimal config
            (root / "config.yaml").write_text(
                "\n".join(
                    [
                        "ocr:",
                        "  api_base: https://example.com/v1",
                        "  api_key: test-key",
                        "  model: test-model",
                    ]
                ),
                encoding="utf-8",
            )

            # Two sheets with 2 + 1 rows
            Image.new("RGB", (100, 50), (0, 0, 0)).save(root / "sheet_0001.png")
            Image.new("RGB", (100, 30), (0, 0, 0)).save(root / "sheet_0002.png")
            mapping = {
                "items": [
                    {"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
                    {"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
                    {"cue_index": 3, "start_ms": 2000, "end_ms": 3000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
                ]
            }
            (root / "mapping.json").write_text(json.dumps(mapping), encoding="utf-8")
            (root / "timeline.srt").write_text("", encoding="utf-8")

            def fake_sheet_lines(_config, image_path: Path, expected_count: int) -> list[str]:
                if image_path.name == "sheet_0001.png":
                    self.assertEqual(expected_count, 2)
                    return ["line a", "line b"]
                self.assertEqual(expected_count, 1)
                return ["line c"]

            with patch("pixelsup.ocr.ocr_sheet_lines", side_effect=fake_sheet_lines):
                out = run_ocr_on_output(root, root / "config.yaml", overwrite_timeline=True)

            self.assertEqual(out.name, "timeline.srt")
            text = out.read_text(encoding="utf-8")
            self.assertIn("line a", text)
            self.assertIn("line b", text)
            self.assertIn("line c", text)

    def test_run_ocr_preserves_multiline_return_for_single_cue(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "config.yaml").write_text(
                "\n".join(
                    [
                        "ocr:",
                        "  api_base: https://example.com/v1",
                        "  api_key: test-key",
                        "  model: test-model",
                    ]
                ),
                encoding="utf-8",
            )
            Image.new("RGB", (100, 30), (0, 0, 0)).save(root / "sheet_0001.png")
            mapping = {
                "items": [
                    {"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
                ]
            }
            (root / "mapping.json").write_text(json.dumps(mapping), encoding="utf-8")
            (root / "timeline.srt").write_text("", encoding="utf-8")

            with patch("pixelsup.ocr.ocr_sheet_lines", return_value=["line1\nline2"]):
                out = run_ocr_on_output(root, root / "config.yaml", overwrite_timeline=True)

            text = out.read_text(encoding="utf-8")
            self.assertIn("line1\nline2", text)

    def test_run_ocr_retries_on_transient_failure(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "config.yaml").write_text(
                "\n".join(
                    [
                        "ocr:",
                        "  api_base: https://example.com/v1",
                        "  api_key: test-key",
                        "  model: test-model",
                        "  max_retries: 2",
                        "  retry_backoff_seconds: 0",
                    ]
                ),
                encoding="utf-8",
            )
            Image.new("RGB", (100, 30), (0, 0, 0)).save(root / "sheet_0001.png")
            mapping = {
                "items": [
                    {"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
                ]
            }
            (root / "mapping.json").write_text(json.dumps(mapping), encoding="utf-8")
            (root / "timeline.srt").write_text("", encoding="utf-8")

            calls = {"n": 0}

            def flaky(_config, _image_path: Path, _expected_count: int) -> list[str]:
                calls["n"] += 1
                if calls["n"] == 1:
                    raise RuntimeError("temporary")
                return ["ok"]

            with patch("pixelsup.ocr.ocr_sheet_lines", side_effect=flaky):
                run_ocr_on_output(root, root / "config.yaml", overwrite_timeline=True)

            self.assertEqual(calls["n"], 2)


if __name__ == "__main__":
    unittest.main()
