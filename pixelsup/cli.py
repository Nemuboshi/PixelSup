from __future__ import annotations

import argparse
import shutil
from collections.abc import Callable
from pathlib import Path

from PIL import Image
from rich.progress import BarColumn, Progress, TextColumn, TimeElapsedColumn, TimeRemainingColumn

from .compose import compose_sheets
from .idx_parser import parse_idx_sub
from .image_ops import add_inner_padding, autocrop_nontransparent, force_white_foreground, resize_to_max_width
from .models import SubtitleCue
from .ocr import run_ocr_on_output
from .sup_parser import parse_sup
from .timeline import write_mapping_json, write_srt

SUPPORTED_IMAGE_SEQUENCE_SUFFIXES = {".png", ".jpg"}


def build_parser_command() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="pixelsup parser",
        description=(
            "Convert SUP or IDX/SUB subtitle streams into vertical sprite sheets and timeline files.\n"
            "Output includes: sheet_*.png, timeline.srt, and mapping.json."
        ),
        epilog=(
            "Examples:\n"
            "  pixelsup parser input.sup\n"
            "  pixelsup parser input.idx\n"
            "  pixelsup parser ./frames_png\n"
            "  pixelsup parser input.sup --limit 20 --gap 12 --padding 10 --max-width 1080 --keep-temp\n"
            "  pixelsup parser input.idx --output ./out --force-white"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "input",
        type=Path,
        help="Input path: .sup file, .idx file (with matching .sub), or a folder of consecutively numbered .png/.jpg files.",
    )
    parser.add_argument(
        "-o",
        "--output",
        type=Path,
        default=None,
        help="Output directory. Defaults to a folder with the same name as the input file (without extension).",
    )
    parser.add_argument(
        "--keep-temp",
        action="store_true",
        help="Keep intermediate cue images in <output>/temp for debugging and inspection.",
    )
    parser.add_argument(
        "--gap",
        type=int,
        default=12,
        help="Vertical gap (in white pixels) between subtitle rows inside each sheet. Default: 12.",
    )
    parser.add_argument(
        "--limit",
        type=int,
        default=40,
        help="Maximum number of subtitle cues per sheet image. Default: 40.",
    )
    parser.add_argument(
        "--max-width",
        type=int,
        default=1080,
        help="Maximum subtitle width after proportional resize. Default: 1080.",
    )
    parser.add_argument(
        "--padding",
        type=int,
        default=10,
        help="Extra transparent pixels to expand around each cropped subtitle image. Default: 10.",
    )
    parser.add_argument(
        "--force-white",
        action="store_true",
        help="Force subtitle foreground pixels to white (preserve alpha).",
    )
    parser.add_argument(
        "--no-row-index",
        action="store_false",
        dest="show_row_index",
        help="Disable row index labels on the left side of composed sheets.",
    )
    parser.set_defaults(show_row_index=True)
    return parser


def build_ocr_command() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="pixelsup ocr",
        description="Run OCR on composed sheet images and backfill recognized text into timeline.srt.",
    )
    parser.add_argument("output_dir", type=Path, help="Directory containing sheet_*.png, mapping.json, and timeline.srt.")
    parser.add_argument(
        "--config",
        type=Path,
        default=Path("ocr_config.yaml"),
        help="Path to OCR config YAML (OpenAI-compatible API settings). Default: ./ocr_config.yaml",
    )
    parser.add_argument(
        "--write-new",
        action="store_true",
        help="Write timeline.ocr.srt instead of overwriting timeline.srt.",
    )
    return parser


def build_root_parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(
        prog="pixelsup",
        description="Subtitle tooling: parse subtitle streams and OCR composed sheets.",
    )
    subparsers = root.add_subparsers(dest="command", required=True)

    parser_cmd = build_parser_command()
    parser_sub = subparsers.add_parser(
        "parser",
        help="Parse .sup or .idx+.sub and compose sheet images + timeline files.",
        description=parser_cmd.description,
        epilog=parser_cmd.epilog,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    for action in parser_cmd._actions:
        if action.dest in ("help",):
            continue
        parser_sub._add_action(action)

    ocr_cmd = build_ocr_command()
    ocr_sub = subparsers.add_parser(
        "ocr",
        help="Run OCR on composed sheets and backfill timeline text.",
        description=ocr_cmd.description,
    )
    for action in ocr_cmd._actions:
        if action.dest in ("help",):
            continue
        ocr_sub._add_action(action)

    return root


def _prepare_output_dir(output_dir: Path, keep_temp: bool) -> Path | None:
    output_dir.mkdir(parents=True, exist_ok=True)
    for stale in output_dir.glob("sheet_*.png"):
        stale.unlink()
    for stale_name in ("timeline.srt", "mapping.json"):
        stale = output_dir / stale_name
        if stale.exists():
            stale.unlink()

    temp_dir = output_dir / "temp"
    if keep_temp:
        if temp_dir.exists():
            shutil.rmtree(temp_dir)
        temp_dir.mkdir(parents=True, exist_ok=True)
        return temp_dir
    if temp_dir.exists():
        shutil.rmtree(temp_dir)
    return None


def _collect_numbered_images(input_dir: Path) -> list[tuple[int, Path]]:
    image_files = [p for p in input_dir.iterdir() if p.is_file() and p.suffix.lower() in SUPPORTED_IMAGE_SEQUENCE_SUFFIXES]
    if not image_files:
        raise SystemExit(f"No .png/.jpg files found in directory: {input_dir}")

    suffixes = {p.suffix.lower() for p in image_files}
    if len(suffixes) != 1:
        raise SystemExit("Image directory must use a single format only (all .png or all .jpg).")

    numbered: list[tuple[int, Path]] = []
    for path in image_files:
        if not path.stem.isdigit():
            raise SystemExit(f"Image filename must be purely numeric (e.g. 001.png): {path.name}")
        numbered.append((int(path.stem), path))

    numbered.sort(key=lambda item: item[0])
    for i in range(1, len(numbered)):
        if numbered[i][0] == numbered[i - 1][0]:
            raise SystemExit(f"Duplicate image number detected: {numbered[i][0]}")

    expected = numbered[0][0]
    for number, _ in numbered:
        if number != expected:
            raise SystemExit(f"Image numbers must be consecutive with no gaps; missing number: {expected}")
        expected += 1

    return numbered


def load_cues_from_image_dir(
    input_dir: Path,
    progress_cb: Callable[[int, int], None] | None = None,
) -> list[SubtitleCue]:
    numbered_images = _collect_numbered_images(input_dir)
    cues: list[SubtitleCue] = []
    total = len(numbered_images)
    for done, (number, image_path) in enumerate(numbered_images, start=1):
        with Image.open(image_path) as image:
            rgba = image.convert("RGBA")
        cues.append(
            SubtitleCue(
                index=number,
                start_ms=(done - 1) * 1000,
                end_ms=done * 1000,
                image=rgba,
            )
        )
        if progress_cb is not None:
            progress_cb(done, total)
    return cues


def _process_cues(
    cues: list[SubtitleCue],
    max_width: int,
    force_white: bool,
    padding: int,
    use_solid_bg_crop_fallback: bool,
    temp_dir: Path | None,
    progress_cb: Callable[[int, int], None] | None = None,
) -> list[SubtitleCue]:
    processed: list[SubtitleCue] = []
    total = len(cues)
    for idx, cue in enumerate(cues, start=1):
        image = autocrop_nontransparent(cue.image, solid_bg_fallback=use_solid_bg_crop_fallback)
        image = add_inner_padding(image, padding)
        image = resize_to_max_width(image, max_width)
        if force_white:
            image = force_white_foreground(image)
        if temp_dir is not None:
            image.save(temp_dir / f"cue_{cue.index:05d}.png")
        processed.append(SubtitleCue(index=cue.index, start_ms=cue.start_ms, end_ms=cue.end_ms, image=image))
        if progress_cb is not None:
            progress_cb(idx, total)
    return processed


def run(args: argparse.Namespace) -> int:
    input_path: Path = args.input
    if not input_path.exists():
        raise SystemExit(f"Input path not found: {input_path}")
    if not input_path.is_file() and not input_path.is_dir():
        raise SystemExit(f"Input path must be a file or directory: {input_path}")
    if input_path.is_file() and input_path.suffix.lower() not in (".sup", ".idx"):
        raise SystemExit("Input file must be a .sup file or .idx file, or provide a directory of numbered images.")
    if args.limit <= 0:
        raise SystemExit("--limit must be > 0")
    if args.gap < 0:
        raise SystemExit("--gap must be >= 0")
    if args.max_width <= 0:
        raise SystemExit("--max-width must be > 0")
    if args.padding < 0:
        raise SystemExit("--padding must be >= 0")

    if args.output is not None:
        output_dir = args.output
    elif input_path.is_dir():
        output_dir = input_path.parent / f"{input_path.name}_out"
    else:
        output_dir = input_path.with_suffix("")
    temp_dir = _prepare_output_dir(output_dir, args.keep_temp)

    with Progress(
        TextColumn("[bold cyan]{task.description}"),
        BarColumn(),
        TextColumn("{task.percentage:>6.2f}%"),
        TimeElapsedColumn(),
        TimeRemainingColumn(),
    ) as progress:
        if input_path.is_dir():
            parse_task = progress.add_task("Image Sequence Loading", total=1)
            cues = load_cues_from_image_dir(
                input_path,
                progress_cb=lambda done, total: progress.update(parse_task, completed=done, total=total),
            )
        else:
            parse_task = progress.add_task("Subtitle Parsing", total=input_path.stat().st_size)
            if input_path.suffix.lower() == ".sup":
                cues = parse_sup(
                    input_path,
                    progress_cb=lambda done, total: progress.update(parse_task, completed=done, total=total),
                )
            else:
                cues = parse_idx_sub(
                    input_path,
                    progress_cb=lambda done, total: progress.update(parse_task, completed=done, total=total),
                )
        if not cues:
            raise SystemExit("No subtitle cues decoded from input.")

        prep_task = progress.add_task("Image Preprocessing", total=len(cues))
        cues = _process_cues(
            cues,
            max_width=args.max_width,
            force_white=args.force_white,
            padding=args.padding,
            use_solid_bg_crop_fallback=input_path.is_dir(),
            temp_dir=temp_dir,
            progress_cb=lambda done, total: progress.update(prep_task, completed=done, total=total),
        )

        total_sheets = (len(cues) + args.limit - 1) // args.limit
        compose_task = progress.add_task("Sheet Composition", total=total_sheets)
        sheets, placements = compose_sheets(
            cues,
            limit=args.limit,
            padding=args.gap,
            show_row_index=args.show_row_index,
            progress_cb=lambda done, total: progress.update(compose_task, completed=done, total=total),
        )

    for sheet in sheets:
        sheet.image.save(output_dir / sheet.name)

    write_srt(cues, placements, output_dir / "timeline.srt")
    write_mapping_json(cues, placements, output_dir / "mapping.json")
    return 0


def run_ocr(args: argparse.Namespace) -> int:
    with Progress(
        TextColumn("[bold cyan]{task.description}"),
        BarColumn(),
        TextColumn("{task.percentage:>6.2f}%"),
        TimeElapsedColumn(),
        TimeRemainingColumn(),
        TextColumn("{task.fields[current_sheet]}"),
    ) as progress:
        task_id = progress.add_task("OCR Backfill", total=100, current_sheet="")

        def on_progress(done: int, total: int, sheet_name: str) -> None:
            progress.update(task_id, completed=done, total=total, current_sheet=sheet_name)

        out_path = run_ocr_on_output(
            output_dir=args.output_dir,
            config_path=args.config,
            overwrite_timeline=not args.write_new,
            progress_cb=on_progress,
        )
    print(f"OCR timeline written to: {out_path}")
    return 0


def main() -> None:
    parser = build_root_parser()
    args = parser.parse_args()
    if args.command == "parser":
        raise SystemExit(run(args))
    if args.command == "ocr":
        raise SystemExit(run_ocr(args))
    raise SystemExit("Unknown command")


if __name__ == "__main__":
    main()
