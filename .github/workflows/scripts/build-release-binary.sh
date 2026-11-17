#!/usr/bin/env bash
set -euo pipefail

# Build one release artifact plus its checksum. linux targets link sqlite
# statically and carry no build id; darwin and windows use the toolchain's
# default sqlite linkage because a static one needs a cross C toolchain.
#
# Usage: bash .github/workflows/scripts/build-release-binary.sh <goos> <goarch> <version> [outdir]

if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
  echo "usage: $0 <goos> <goarch> <version> [outdir]" >&2
  exit 2
fi

goos="$1"
goarch="$2"
version="${3#v}"
outdir="${4:-dist}"

case "$goos/$goarch" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64) ;;
  *)
    echo "error: $goos/$goarch is not a release platform (linux/{amd64,arm64}, darwin/{amd64,arm64}, windows/amd64)" >&2
    exit 2
    ;;
esac

if [ -z "$version" ]; then
  echo "error: version must be non-empty" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
ui_dir="$root/transports/bifrost-http/ui"

if [ ! -f "$ui_dir/index.html" ]; then
  echo "error: $ui_dir has no build output; run 'make build-ui' first" >&2
  exit 1
fi

name="marga-runtime-${goos}-${goarch}"
if [ "$goos" = "windows" ]; then
  name="${name}.exe"
fi

mkdir -p "$outdir"
artifact="$outdir/$name"

ldflags="-w -s -X main.Version=v${version}"
tags=""
if [ "$goos" = "linux" ]; then
  ldflags="$ldflags -extldflags '-static' -buildid="
  tags="-tags sqlite_static"
fi

(
  cd "$root/transports/bifrost-http"
  # shellcheck disable=SC2086 # tags and ldflags are intentionally word-split
  CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath $tags -ldflags "$ldflags" -o "$artifact" .
)

if [ ! -f "$artifact" ]; then
  echo "error: go build produced no $artifact" >&2
  exit 1
fi

(
  cd "$outdir"
  if command -v sha256sum >/dev/null 2>&1; then
    digest="$(sha256sum "$name")"
  else
    digest="$(shasum -a 256 "$name")"
  fi
  printf '%s  %s\n' "${digest%% *}" "$name" > "$name.sha256"
)

echo "built $artifact"
