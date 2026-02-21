from __future__ import annotations

from PIL import Image, ImageChops


def _autocrop_solid_background(rgba: Image.Image) -> Image.Image:
    width, height = rgba.size
    corners = {
        rgba.getpixel((0, 0)),
        rgba.getpixel((width - 1, 0)),
        rgba.getpixel((0, height - 1)),
        rgba.getpixel((width - 1, height - 1)),
    }
    if len(corners) != 1:
        return rgba

    background = corners.pop()
    bg_image = Image.new("RGBA", rgba.size, background)
    diff = ImageChops.difference(rgba, bg_image)
    bbox = diff.convert("RGB").getbbox()
    if bbox is None:
        return rgba
    return rgba.crop(bbox)


def autocrop_nontransparent(image: Image.Image, solid_bg_fallback: bool = False) -> Image.Image:
    rgba = image.convert("RGBA")
    alpha = rgba.getchannel("A")
    bbox = alpha.getbbox()
    if bbox is None:
        return rgba
    full_bbox = (0, 0, rgba.width, rgba.height)
    if bbox != full_bbox:
        return rgba.crop(bbox)
    if not solid_bg_fallback:
        return rgba
    return _autocrop_solid_background(rgba)


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
    alpha = rgba.getchannel("A")
    width, height = rgba.size
    for y in range(height):
        for x in range(width):
            alpha_value = alpha.getpixel((x, y))
            if isinstance(alpha_value, tuple):
                a = int(alpha_value[0]) if alpha_value else 0
            elif isinstance(alpha_value, (int, float)):
                a = int(alpha_value)
            else:
                a = 0
            if a:
                rgba.putpixel((x, y), (255, 255, 255, a))
    return rgba
