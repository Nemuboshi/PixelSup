from __future__ import annotations

from PIL import Image


def autocrop_nontransparent(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    alpha = rgba.getchannel("A")
    bbox = alpha.getbbox()
    if bbox is None:
        return rgba
    return rgba.crop(bbox)


def resize_to_max_width(image: Image.Image, max_width: int) -> Image.Image:
    if max_width <= 0:
        raise ValueError("max_width must be > 0")
    rgba = image.convert("RGBA")
    width, height = rgba.size
    if width <= max_width:
        return rgba
    scale = max_width / width
    new_height = max(1, int(round(height * scale)))
    return rgba.resize((max_width, new_height), Image.Resampling.LANCZOS)


def add_inner_padding(image: Image.Image, padding: int) -> Image.Image:
    if padding < 0:
        raise ValueError("padding must be >= 0")
    rgba = image.convert("RGBA")
    if padding == 0:
        return rgba
    width, height = rgba.size
    canvas = Image.new("RGBA", (width + padding * 2, height + padding * 2), (0, 0, 0, 0))
    canvas.paste(rgba, (padding, padding), rgba)
    return canvas


def force_white_foreground(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    px = rgba.load()
    width, height = rgba.size
    for y in range(height):
        for x in range(width):
            _, _, _, a = px[x, y]
            if a:
                px[x, y] = (255, 255, 255, a)
    return rgba
