## PixelSup

Convert Blu-ray `.sup` subtitles into:
- vertical sprite sheets (`sheet_0001.png`, ...)
- `timeline.srt` (per-cue time with image marker text)
- `mapping.json` (machine-readable mapping)

Also supports VobSub `.idx + .sub` input.

## Install

Recommended: use `uv`.

```bash
uv sync
```

No Python environment:
- Download prebuilt binaries from GitHub `Releases`.

## Usage

```bash
uv run -m pixelsup.cli parser input.sup --limit 40 --gap 12 --max-width 1080 --padding 10 --keep-temp
uv run -m pixelsup.cli parser input.idx --limit 40 --gap 12 --max-width 1080 --padding 10
uv run -m pixelsup.cli ocr ./output_dir --config ./ocr_config.yaml
```

By default, each sheet includes large row index labels in a left-side gutter.
Use `--no-row-index` to disable this.

Or install and use the script entrypoint:

```bash
pixelsup parser input.sup -o output_dir
pixelsup ocr output_dir --config ocr_config.yaml
```

## Output

- `sheet_*.png`: black background, white spacing between rows
- `timeline.srt`: each subtitle keeps original timing and uses marker text like `[img:sheet_0001.png#03]`
- `mapping.json`: per-cue mapping (sheet, position, start/end ms)
- `temp/*.png`: kept only with `--keep-temp`
