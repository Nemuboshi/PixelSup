import tempfile
import unittest
from pathlib import Path

from PIL import Image

from pixelsup.cli import load_cues_from_image_dir


def _write_image(path: Path) -> None:
    Image.new("RGB", (8, 6), (255, 255, 255)).save(path)


class CliImageDirInputTest(unittest.TestCase):
    def test_load_cues_from_numbered_png_directory(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            image_dir = Path(tmp)
            _write_image(image_dir / "001.png")
            _write_image(image_dir / "002.png")
            _write_image(image_dir / "003.png")

            cues = load_cues_from_image_dir(image_dir)

            self.assertEqual([cue.index for cue in cues], [1, 2, 3])
            self.assertEqual([cue.start_ms for cue in cues], [0, 1000, 2000])
            self.assertEqual([cue.end_ms for cue in cues], [1000, 2000, 3000])

    def test_load_cues_rejects_mixed_formats(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            image_dir = Path(tmp)
            _write_image(image_dir / "001.png")
            _write_image(image_dir / "002.jpg")

            with self.assertRaises(SystemExit):
                load_cues_from_image_dir(image_dir)

    def test_load_cues_rejects_non_consecutive_numbers(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            image_dir = Path(tmp)
            _write_image(image_dir / "001.jpg")
            _write_image(image_dir / "003.jpg")

            with self.assertRaises(SystemExit):
                load_cues_from_image_dir(image_dir)

    def test_load_cues_rejects_non_numeric_filename(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            image_dir = Path(tmp)
            _write_image(image_dir / "foo.png")

            with self.assertRaises(SystemExit):
                load_cues_from_image_dir(image_dir)


if __name__ == "__main__":
    unittest.main()
