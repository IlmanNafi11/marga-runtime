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

# Resolve a relative outdir against the caller's working directory once, before
# the build's own `cd`. `go build -o` runs inside transports/bifrost-http, so
# leaving it relative wrote the artifact one directory down and every check
# afterwards reported "go build produced no dist-spike/...".
case "$outdir" in
  /*) ;;
  *) outdir="$PWD/$outdir" ;;
esac

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

# CGO means a C cross compiler per target. install-cross-compilers.sh provides
# all five; leaving CC unset let Go fall back to the runner's native gcc, which
# aborts on `windows.h: No such file or directory` for windows and links the
# host ABI for darwin.
cc=""
cxx=""
case "$goos/$goarch" in
  linux/amd64)
    ldflags="$ldflags -extldflags '-static' -buildid="
    tags="-tags sqlite_static"
    cc=x86_64-linux-musl-gcc
    cxx=x86_64-linux-musl-g++
    ;;
  linux/arm64)
    ldflags="$ldflags -extldflags '-static' -buildid="
    tags="-tags sqlite_static"
    cc=aarch64-linux-musl-gcc
    cxx=aarch64-linux-musl-g++
    ;;
  darwin/amd64)
    cc=o64-clang
    cxx=o64-clang++
    ;;
  darwin/arm64)
    cc=oa64-clang
    cxx=oa64-clang++
    ;;
  windows/amd64)
    cc=x86_64-w64-mingw32-gcc
    cxx=x86_64-w64-mingw32-g++
    ;;
esac

(
  cd "$root/transports/bifrost-http"
  # shellcheck disable=SC2086 # tags and ldflags are intentionally word-split
  CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" CC="$cc" CXX="$cxx" \
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
