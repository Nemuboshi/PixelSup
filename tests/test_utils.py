import unittest

from pixelsup.utils import chunked, format_srt_timestamp


class UtilsTest(unittest.TestCase):
    def test_format_srt_timestamp(self) -> None:
        self.assertEqual(format_srt_timestamp(0), "00:00:00,000")
        self.assertEqual(format_srt_timestamp(3723004), "01:02:03,004")

    def test_chunked(self) -> None:
        self.assertEqual(list(chunked([1, 2, 3, 4, 5], 2)), [[1, 2], [3, 4], [5]])


if __name__ == "__main__":
    unittest.main()