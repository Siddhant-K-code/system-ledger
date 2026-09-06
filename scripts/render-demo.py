#!/usr/bin/env python3
"""Render a GIF from command output captured by record-demo.sh.

The command lines are fixed demo inputs; every displayed result is read from
the clean project's real system-ledger command output.
"""

import argparse
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

WIDTH, HEIGHT = 1100, 680
PADDING = 30
FONT_SIZE = 19
LINE_HEIGHT = 27
BACKGROUND = "#282a36"
TEXT = "#f8f8f2"
PROMPT = "#8be9fd"
COMMAND = "#50fa7b"


def font(size):
    for candidate in (
        "/System/Library/Fonts/Menlo.ttc",
        "/System/Library/Fonts/Supplemental/Menlo.ttc",
    ):
        if Path(candidate).exists():
            return ImageFont.truetype(candidate, size)
    return ImageFont.load_default()


def screen(command, output, typed=""):
    image = Image.new("RGB", (WIDTH, HEIGHT), BACKGROUND)
    draw = ImageDraw.Draw(image)
    regular = font(FONT_SIZE)
    cursor = "$ " + typed
    draw.text((PADDING, PADDING), "$ ", font=regular, fill=PROMPT)
    draw.text((PADDING + draw.textlength("$ ", font=regular), PADDING), typed, font=regular, fill=COMMAND)
    if typed != command:
        return image
    lines = output.rstrip().splitlines()
    max_lines = (HEIGHT - PADDING * 2) // LINE_HEIGHT - 1
    for index, line in enumerate(lines[:max_lines]):
        draw.text((PADDING, PADDING + (index + 1) * LINE_HEIGHT), line, font=regular, fill=TEXT)
    return image


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--logs", type=Path, required=True)
    parser.add_argument("--gif", type=Path, required=True)
    parser.add_argument("--preview", type=Path, required=True)
    parser.add_argument("--transcript", type=Path, required=True)
    args = parser.parse_args()

    commands = [
        ("scan", "system-ledger scan"),
        ("build", "system-ledger build"),
        ("summary", "system-ledger summary"),
        ("path", "system-ledger path listProducts product"),
        ("doctor", "system-ledger doctor"),
    ]
    frames, durations, transcript = [], [], []
    for name, command in commands:
        output = (args.logs / f"{name}.txt").read_text(encoding="utf-8")
        frames.append(screen(command, output, command[: max(1, len(command) // 2)]))
        durations.append(600)
        frames.append(screen(command, output, command))
        durations.append(3500 if name != "doctor" else 5000)
        transcript.extend([f"$ {command}", output.rstrip(), ""])

    args.gif.parent.mkdir(parents=True, exist_ok=True)
    frames[0].save(args.gif, save_all=True, append_images=frames[1:], duration=durations, loop=0, optimize=True)
    frames[-1].save(args.preview)
    args.transcript.write_text("\n".join(transcript).rstrip() + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
