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
<binary> parse input.sup -o out
<binary> parse input.idx -o out
<binary> export input.idx -o out_export
<binary> ocr out --config ./ocr_config.yaml
<binary> ocr out --config ./ocr_config.yaml --strict
```

## Commands

```text
parse   Parse .sup, .idx(+.sub), or image dirs into sheets + timeline outputs.
export  Export .sup/.idx cues as per-cue PNG files with timeline outputs.
ocr     Run OCR on composed sheets and write subtitle text.
```

`ocr` provider is selected in `ocr_config.yaml` via `ocr.provider`.
Both providers now read the digits-composed `sheet_*.png` output and split subtitle lines with the embedded `0123456789` separator.

Sample config structure:

```yaml
ocr:
  provider: "openai_llm"
  max_concurrency: 4

openai_llm:
  api_base: "https://api.example.com/v1"
  api_key: "token-test"
  model: "A/B-C"
  max_tokens: 8192

paddle_ocr:
  api_url: "https://q68drf0aje9ay2rd.aistudio-app.com/ocr"
  token: "token-test"
```

Use help for full options:

```bash
<binary> --help
<binary> parse --help
<binary> export --help
<binary> ocr --help
```

## Optional: Build from Source

```bash
go build ./cmd/pixelsup
```

