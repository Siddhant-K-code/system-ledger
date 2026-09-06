#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
vhs_bin="${VHS_BIN:-vhs}"
workspace="$(mktemp -d)"
binary="$workspace/system-ledger"
project="$workspace/project"
logs="$workspace/logs"

cleanup() {
  rm -rf "$workspace"
}
trap cleanup EXIT

require() {
  command -v "$1" >/dev/null || {
    echo "Required recorder dependency is unavailable: $1" >&2
    exit 1
  }
}

require "$vhs_bin"
require ffmpeg
require ttyd

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

{
  for name in scan build summary path doctor; do
    case "$name" in
      scan) command="system-ledger scan" ;;
      build) command="system-ledger build" ;;
      summary) command="system-ledger summary" ;;
      path) command="system-ledger path listProducts product" ;;
      doctor) command="system-ledger doctor" ;;
    esac
    printf '$ %s\n' "$command"
    cat "$logs/$name.txt"
    if [[ "$name" != "doctor" ]]; then
      printf '\n'
    fi
  done
} >"$root/docs/assets/demo-transcript.txt"

(
  cd "$project"
  PATH="$workspace:$PATH" "$vhs_bin" "$root/docs/assets/demo.tape" \
    --output "$root/docs/assets/demo.gif" --quiet
)

ffmpeg -y -ss 13 -i "$root/docs/assets/demo.gif" -frames:v 1 \
    "$root/docs/assets/demo-preview.png" >/dev/null 2>&1
