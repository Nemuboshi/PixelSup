import unittest

from pixelsup.sup_parser import _decode_rle_indices


class SupParserRLETest(unittest.TestCase):
    def test_rle_explicit_end_of_line_does_not_insert_blank_rows(self) -> None:
        # Two lines, each with 4 pixels of color index 1, each line terminated by 00 00.
        data = bytes([0x00, 0x84, 0x01, 0x00, 0x00, 0x00, 0x84, 0x01, 0x00, 0x00])
        decoded = _decode_rle_indices(data, width=4, height=2)
        self.assertEqual(decoded, [1, 1, 1, 1, 1, 1, 1, 1])


if __name__ == "__main__":
    unittest.main()
