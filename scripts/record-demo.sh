#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workspace="$(mktemp -d)"
binary="$workspace/system-ledger"
project="$workspace/project"
logs="$workspace/logs"
python_bin="${PYTHON_BIN:-python3}"

cleanup() {
  rm -rf "$workspace"
}
trap cleanup EXIT

go build -o "$binary" "$root/cmd/system-ledger"
mkdir -p "$project" "$logs"
cp -R "$root/examples/multi-service/." "$project/"

capture() {
  local name="$1"
  shift
  (
    cd "$project"
    NO_COLOR=1 "$binary" "$@"
  ) >"$logs/$name.txt" 2>&1
}

capture scan scan
capture build build
capture summary summary
capture path path listProducts product
capture doctor doctor

"$python_bin" -c 'from PIL import Image' 2>/dev/null || {
  echo "Pillow is required. Set PYTHON_BIN to a Python environment with Pillow installed." >&2
  exit 1
}

"$python_bin" "$root/scripts/render-demo.py" \
  --logs "$logs" \
  --gif "$root/docs/assets/demo.gif" \
  --preview "$root/docs/assets/demo-preview.png" \
  --transcript "$root/docs/assets/demo-transcript.txt"
