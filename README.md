## PixelSup

PixelSup converts subtitle sources into image sheets and timeline files.

Supported input:
- Blu-ray `.sup`
- VobSub `.idx + .sub`

Main output:
- `sheet_*.png`
- `timeline.srt`
- `mapping.json`

## Quick Start (Prebuilt Binary)

1. Download the latest binary from GitHub `Releases`.
2. Unzip it and run from terminal.

Examples:

```bash
<binary> parser input.sup -o out
<binary> parser input.idx -o out
<binary> export input.idx -o out_export
<binary> ocr out --config ./ocr_config.yaml
```

## Commands

```text
parser  Parse .sup, .idx(+.sub), or image dirs into sheets + timeline outputs.
export  Export .sup/.idx cues as per-cue PNG files with timeline outputs.
ocr     Run OCR on composed sheets and write subtitle text.
```

Use help for full options:

```bash
<binary> --help
<binary> parser --help
<binary> export --help
<binary> ocr --help
```

## Optional: Build from Source

```bash
go build ./cmd/pixelsup
```
