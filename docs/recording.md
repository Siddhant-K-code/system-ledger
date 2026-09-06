# Recording the CLI demo

`docs/assets/demo.gif` is an actual recording of the checked-in
`examples/multi-service` project. It shows only output from a locally built
binary at the current commit. The matching accessible transcript is
[`docs/assets/demo-transcript.txt`](assets/demo-transcript.txt), and
`demo-preview.png` is the GIF's final frame.

## Regenerate

The portable recording source is [`assets/demo.tape`](assets/demo.tape). It is
validated with [Charmbracelet VHS](https://github.com/charmbracelet/vhs) and
uses a 1100×680 viewport, 19px Menlo text, and clear command boundaries:

```sh
vhs validate docs/assets/demo.tape
```

When VHS and `ffmpeg` are installed, render the tape directly:

```sh
cd examples/multi-service
PATH="/path/to/locally-built-binary:$PATH" vhs ../../docs/assets/demo.tape \
  --output ../../docs/assets/demo.gif
```

The committed media was generated through the reproducible fallback below
because the capture environment had VHS but no `ffmpeg` encoder. It creates a
temporary project copy, runs the actual commands, captures their output, and
renders the terminal frames using Pillow. It never changes the example,
manifest, or ledger in the working tree.

```sh
python3 -m venv /tmp/system-ledger-demo-venv
/tmp/system-ledger-demo-venv/bin/pip install Pillow
PYTHON_BIN=/tmp/system-ledger-demo-venv/bin/python scripts/record-demo.sh
```

The helper requires Go and Python 3 with Pillow. VHS rendering requires VHS
and `ffmpeg`; `ttyd` is not needed for this noninteractive command sequence.
