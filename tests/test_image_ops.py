import unittest

from PIL import Image

from pixelsup.image_ops import add_inner_padding, autocrop_nontransparent, resize_to_max_width


class ImageOpsTest(unittest.TestCase):
    def test_autocrop_nontransparent(self) -> None:
        img = Image.new("RGBA", (10, 10), (0, 0, 0, 0))
        for x in range(2, 6):
            for y in range(3, 8):
                img.putpixel((x, y), (255, 255, 255, 255))

        cropped = autocrop_nontransparent(img)
        self.assertEqual(cropped.size, (4, 5))

    def test_resize_to_max_width(self) -> None:
        img = Image.new("RGBA", (2000, 1000), (255, 255, 255, 255))
        resized = resize_to_max_width(img, 1000)
        self.assertEqual(resized.size, (1000, 500))

    def test_add_inner_padding(self) -> None:
        img = Image.new("RGBA", (20, 10), (255, 255, 255, 255))
        padded = add_inner_padding(img, 10)
        self.assertEqual(padded.size, (40, 30))


if __name__ == "__main__":
    unittest.main()
