import json
import tempfile
import unittest
from pathlib import Path

from PIL import Image

from pixelsup.models import CuePlacement, SubtitleCue
from pixelsup.timeline import write_mapping_json, write_srt


class TimelineTest(unittest.TestCase):
    def test_write_srt_and_mapping(self) -> None:
        cues = [
            SubtitleCue(index=1, start_ms=0, end_ms=1200, image=Image.new("RGBA", (10, 10), (255, 255, 255, 255))),
            SubtitleCue(index=2, start_ms=1200, end_ms=2600, image=Image.new("RGBA", (10, 10), (255, 255, 255, 255))),
        ]
        placements = {
            1: CuePlacement(sheet_name="sheet_0001.png", position_in_sheet=1),
            2: CuePlacement(sheet_name="sheet_0001.png", position_in_sheet=2),
        }

        with tempfile.TemporaryDirectory() as tmp:
            srt_path = Path(tmp) / "timeline.srt"
            map_path = Path(tmp) / "mapping.json"

            write_srt(cues, placements, srt_path)
            write_mapping_json(cues, placements, map_path)

            srt_text = srt_path.read_text(encoding="utf-8")
            self.assertIn("[img:sheet_0001.png#01]", srt_text)
            self.assertIn("00:00:00,000 --> 00:00:01,200", srt_text)

            data = json.loads(map_path.read_text(encoding="utf-8"))
            self.assertEqual(len(data["items"]), 2)
            self.assertEqual(data["items"][1]["sheet"], "sheet_0001.png")


if __name__ == "__main__":
    unittest.main()