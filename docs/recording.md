# Recording the CLI demo

`docs/assets/demo.gif` is an actual recording of the checked-in
`examples/multi-service` project. It shows only output from a locally built
binary at the current commit. The matching accessible transcript is
[`docs/assets/demo-transcript.txt`](assets/demo-transcript.txt), and
`demo-preview.png` is the GIF's final frame.

## Regenerate

The source is [`assets/demo.tape`](assets/demo.tape), recorded with
[Charmbracelet VHS](https://github.com/charmbracelet/vhs). The canonical
regeneration command is:

```sh
scripts/record-demo.sh
```

The wrapper requires Go, VHS, `ffmpeg`, and `ttyd`. It builds a temporary
binary, copies `examples/multi-service` into a fresh temporary project, runs
the real commands there, writes `demo.gif`, extracts a path-focused static
preview, writes the corresponding real-output transcript, and removes its
temporary files. It does not modify the example or its working-tree ledger.

Install the recording dependencies with your package manager, then put VHS on
your `PATH`:

```sh
brew install ffmpeg ttyd
go install github.com/charmbracelet/vhs@v0.10.0
export PATH="$(go env GOPATH)/bin:$PATH"
vhs validate docs/assets/demo.tape
scripts/record-demo.sh
```

The tape uses a 1040×500 viewport, 18px Menlo text, visible typed commands,
and output holds. It records `scan`, `build`, `summary`,
`path listProducts product`, and `doctor`.
