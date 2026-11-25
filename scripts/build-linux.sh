#!/usr/bin/env bash
set -euo pipefail

if (( $# > 1 )); then
  printf 'Usage: %s [amd64|arm64]\n' "$0" >&2
  exit 2
fi

target=${1:-$(uname -m)}
case "$target" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *)
    printf 'Unsupported architecture: %s. Use amd64 or arm64.\n' "$target" >&2
    exit 2
    ;;
esac

export PATH="$PATH:/usr/local/go/bin"
if ! command -v go >/dev/null; then
  printf 'Go is required. Install the version specified in go.mod and add it to PATH.\n' >&2
  exit 1
fi

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
mkdir -p dist
build_dir=$(mktemp -d dist/.build-linux.XXXXXX)
trap 'rm -rf -- "$build_dir"' EXIT

printf 'Building linux/%s...\n' "$arch"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
  go build -trimpath -ldflags="-s -w" -o "$build_dir/wb2api" ./cmd/server
mv -f -- "$build_dir/wb2api" dist/wb2api
printf 'Created %s/dist/wb2api\n' "$PWD"
