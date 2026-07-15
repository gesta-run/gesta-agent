#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  printf 'usage: %s output-dir\n' "$0" >&2
  exit 2
fi

out_dir=$1
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
targets=${GESTA_AGENT_TARGETS:-"darwin/amd64 darwin/arm64 linux/amd64 linux/arm64"}

mkdir -p "$out_dir"
abs_out_dir=$(CDPATH= cd -- "$out_dir" && pwd)

for target in $targets; do
  goos=${target%/*}
  goarch=${target#*/}
  case "$goos/$goarch" in
    darwin/amd64|darwin/arm64|linux/amd64|linux/arm64)
      ;;
    *)
      printf 'unsupported agent target: %s\n' "$target" >&2
      exit 2
      ;;
  esac

  out="$abs_out_dir/gesta-agent-$goos-$goarch"
  printf 'building %s\n' "$out"
  (
    cd "$repo_root"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags "-s -w" -o "$out" ./cmd
  )
done
