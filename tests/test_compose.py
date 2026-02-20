import unittest

from PIL import Image

from pixelsup.compose import compose_sheets
from pixelsup.models import SubtitleCue


class ComposeTest(unittest.TestCase):
    def test_compose_uses_non_overlapping_limit_chunks(self) -> None:
        cues = [
            SubtitleCue(index=i + 1, start_ms=i * 1000, end_ms=(i + 1) * 1000, image=Image.new("RGBA", (10, 4), (255, 255, 255, 255)))
            for i in range(254)
        ]
        sheets, placements = compose_sheets(cues, limit=20, padding=2)

        self.assertEqual(len(sheets), 13)
        self.assertEqual(sheets[0].cue_indexes, list(range(1, 21)))
        self.assertEqual(sheets[1].cue_indexes, list(range(21, 41)))
        self.assertEqual(sheets[-1].cue_indexes, list(range(241, 255)))
        self.assertEqual(placements[1].sheet_name, "sheet_0001.png")
        self.assertEqual(placements[20].sheet_name, "sheet_0001.png")
        self.assertEqual(placements[21].sheet_name, "sheet_0002.png")
        self.assertEqual(placements[254].sheet_name, "sheet_0013.png")

    def test_compose_progress_callback(self) -> None:
        cues = [
            SubtitleCue(index=i + 1, start_ms=i * 1000, end_ms=(i + 1) * 1000, image=Image.new("RGBA", (10, 4), (255, 255, 255, 255)))
            for i in range(45)
        ]
        ticks: list[tuple[int, int]] = []

        def on_progress(done: int, total: int) -> None:
            ticks.append((done, total))

        sheets, _ = compose_sheets(cues, limit=20, padding=2, progress_cb=on_progress)
        self.assertEqual(len(sheets), 3)
        self.assertEqual(ticks, [(1, 3), (2, 3), (3, 3)])

    def test_row_index_gutter_expands_canvas_and_adds_white_separator(self) -> None:
        cues = [
            SubtitleCue(index=1, start_ms=0, end_ms=1000, image=Image.new("RGBA", (30, 10), (255, 255, 255, 255))),
            SubtitleCue(index=2, start_ms=1000, end_ms=2000, image=Image.new("RGBA", (30, 10), (255, 255, 255, 255))),
        ]
        sheets, _ = compose_sheets(cues, limit=20, padding=4, show_row_index=True)
        self.assertEqual(len(sheets), 1)
        sheet = sheets[0].image
        self.assertGreater(sheet.width, 30)

        separator_x = 0
        for x in range(sheet.width):
            if sheet.getpixel((x, 0)) == (255, 255, 255):
                separator_x = x
                break
        self.assertGreater(separator_x, 0)

    def test_row_index_can_be_disabled(self) -> None:
        cues = [SubtitleCue(index=1, start_ms=0, end_ms=1000, image=Image.new("RGBA", (30, 10), (255, 255, 255, 255)))]
        sheets, _ = compose_sheets(cues, limit=20, padding=4, show_row_index=False)
        self.assertEqual(sheets[0].image.width, 30)

    def test_row_index_gutter_uses_min_row_height_square(self) -> None:
        cues = [
            SubtitleCue(index=1, start_ms=0, end_ms=1000, image=Image.new("RGBA", (60, 20), (255, 255, 255, 255))),
            SubtitleCue(index=2, start_ms=1000, end_ms=2000, image=Image.new("RGBA", (60, 200), (255, 255, 255, 255))),
        ]
        sheets, _ = compose_sheets(cues, limit=20, padding=6, show_row_index=True)
        sheet = sheets[0].image
        expected_width = 60 + 20 + 6
        self.assertEqual(sheet.width, expected_width)


if __name__ == "__main__":
    unittest.main()
