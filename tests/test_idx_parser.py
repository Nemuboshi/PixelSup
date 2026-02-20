import unittest
import tempfile
from pathlib import Path

from pixelsup.idx_parser import _decode_vobsub_spu, _parse_idx_timestamp, parse_idx_sub


class IdxParserTest(unittest.TestCase):
    def test_parse_idx_timestamp(self) -> None:
        self.assertEqual(_parse_idx_timestamp("01:02:03:456"), 3_723_456)

    def test_decode_vobsub_spu_minimal(self) -> None:
        # Minimal SPU packet:
        # - size=26, control offset=6
        # - two field bytes at offsets 4 and 5, both "fill rest with color 1"
        # - colormap and alpha map color index 1 to opaque palette entry
        packet = bytes(
            [
                0x00,
                0x1E,  # size
                0x00,
                0x06,  # control offset
                0x10,  # field 1 RLE
                0x10,  # field 2 RLE
                0x00,
                0x00,  # date
                0x00,
                0x06,  # next cmd offset (self/end)
                0x03,
                0x11,
                0x11,  # colormap -> all map to palette entry 1
                0x04,
                0xFF,
                0xFF,  # alpha -> all opaque
                0x05,
                0x00,
                0x00,
                0x01,
                0x00,
                0x00,
                0x02,  # x=0..1, y=0..2 (decoder uses y2-y1 for height)
                0x06,
                0x00,
                0x04,
                0x00,
                0x05,  # offsets
                0x01,
                0xFF,
            ]
        )
        palette = [0x000000] * 16
        palette[1] = 0xFFFFFF
        image = _decode_vobsub_spu(packet, palette)
        self.assertEqual(image.size, (2, 2))
        self.assertEqual(image.getpixel((0, 0)), (255, 255, 255, 255))
        self.assertEqual(image.getpixel((1, 1)), (255, 255, 255, 255))

    def test_parse_idx_sub_end_to_end_minimal(self) -> None:
        spu = bytes(
            [
                0x00,
                0x1E,
                0x00,
                0x06,
                0x10,
                0x10,
                0x00,
                0x00,
                0x00,
                0x06,
                0x03,
                0x11,
                0x11,
                0x04,
                0xFF,
                0xFF,
                0x05,
                0x00,
                0x00,
                0x01,
                0x00,
                0x00,
                0x02,
                0x06,
                0x00,
                0x04,
                0x00,
                0x05,
                0x01,
                0xFF,
            ]
        )
        pes_payload = bytes([0x20]) + spu
        pes_header = bytes([0x80, 0x00, 0x00])
        pes_len = len(pes_header) + len(pes_payload)
        sub_data = b"\x00\x00\x01\xbd" + bytes([(pes_len >> 8) & 0xFF, pes_len & 0xFF]) + pes_header + pes_payload

        idx_text = (
            "# VobSub index file, v7\n"
            "size: 720x480\n"
            "palette: 000000, ffffff, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000\n"
            "timestamp: 00:00:01:000, filepos: 000000000\n"
        )

        with tempfile.TemporaryDirectory() as tmp:
            idx_path = Path(tmp) / "sample.idx"
            sub_path = Path(tmp) / "sample.sub"
            idx_path.write_text(idx_text, encoding="utf-8")
            sub_path.write_bytes(sub_data)

            cues = parse_idx_sub(idx_path)
            self.assertEqual(len(cues), 1)
            self.assertEqual(cues[0].start_ms, 1000)
            self.assertEqual(cues[0].image.size, (2, 2))


if __name__ == "__main__":
    unittest.main()
