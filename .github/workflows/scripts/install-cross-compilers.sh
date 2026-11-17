#!/usr/bin/env bash
set -euo pipefail

# Install the C cross toolchain a release target needs, for Go + CGO.
# Usage: ./install-cross-compilers.sh [linux|darwin|windows]
#   With no argument every toolchain is installed (the cross spike does that).
#
# musl-tools is what provides the real x86_64-linux-musl-gcc / aarch64-linux-musl-gcc
# that build-release-binary.sh exports as CC. This script used to symlink those
# names to the *glibc* gcc in /usr/local/bin, which shadows the real ones on
# PATH and silently links linux artifacts against glibc instead of musl.

target="${1:-all}"
case "$target" in
  linux|darwin|windows|all) ;;
  *)
    echo "error: unknown target '$target' (want linux, darwin, windows or nothing)" >&2
    exit 2
    ;;
esac

want_linux=0
want_darwin=0
want_windows=0
case "$target" in
  linux) want_linux=1 ;;
  darwin) want_darwin=1 ;;
  windows) want_windows=1 ;;
  all) want_linux=1; want_darwin=1; want_windows=1 ;;
esac

packages=()
[ "$want_linux" = 1 ] && packages+=(musl-tools)
[ "$want_windows" = 1 ] && packages+=(gcc-mingw-w64-x86-64)
if [ "$want_darwin" = 1 ]; then
  packages+=(clang lld xz-utils curl)
fi

echo "📦 Installing cross-compilation toolchains for Go + CGO (${target})..."
"$(dirname "${BASH_SOURCE[0]}")/apt-sources-https.sh"
sudo apt-get update
sudo apt-get install -y "${packages[@]}"

if [ "$want_darwin" = 1 ]; then
  echo "🍎 Setting up Darwin cross-compilation..."

  SDK_DIR="/opt/MacOSX12.3.sdk"
  SDK_URL="https://github.com/phracker/MacOSX-SDKs/releases/download/12.3/MacOSX12.3.sdk.tar.xz"

  if [ ! -d "$SDK_DIR" ]; then
    echo "📦 Downloading macOS SDK..."
    # -f fails on HTTP errors, -L follows redirects.
    if ! curl -fL "$SDK_URL" -o /tmp/MacOSX12.3.sdk.tar.xz; then
      echo "❌ Failed to download macOS SDK from primary URL, trying alternative..."
      SDK_URL_ALT="https://github.com/joseluisq/macosx-sdks/releases/download/12.3/MacOSX12.3.sdk.tar.xz"
      curl -fL "$SDK_URL_ALT" -o /tmp/MacOSX12.3.sdk.tar.xz
    fi
    sudo mkdir -p /opt
    sudo tar -xf /tmp/MacOSX12.3.sdk.tar.xz -C /opt
    rm -f /tmp/MacOSX12.3.sdk.tar.xz
  fi

  sudo tee /usr/local/bin/o64-clang > /dev/null << 'WRAPPER_EOF'
#!/bin/bash
exec clang -target x86_64-apple-darwin --sysroot=/opt/MacOSX12.3.sdk -fuse-ld=lld -Wno-unused-command-line-argument "$@"
WRAPPER_EOF

  sudo tee /usr/local/bin/o64-clang++ > /dev/null << 'WRAPPER_EOF'
#!/bin/bash
exec clang++ -target x86_64-apple-darwin --sysroot=/opt/MacOSX12.3.sdk -fuse-ld=lld -Wno-unused-command-line-argument "$@"
WRAPPER_EOF

  sudo tee /usr/local/bin/oa64-clang > /dev/null << 'WRAPPER_EOF'
#!/bin/bash
exec clang -target arm64-apple-darwin --sysroot=/opt/MacOSX12.3.sdk -fuse-ld=lld -Wno-unused-command-line-argument "$@"
WRAPPER_EOF

  sudo tee /usr/local/bin/oa64-clang++ > /dev/null << 'WRAPPER_EOF'
#!/bin/bash
exec clang++ -target arm64-apple-darwin --sysroot=/opt/MacOSX12.3.sdk -fuse-ld=lld -Wno-unused-command-line-argument "$@"
WRAPPER_EOF

  sudo chmod +x /usr/local/bin/o64-clang /usr/local/bin/o64-clang++ \
                 /usr/local/bin/oa64-clang /usr/local/bin/oa64-clang++
  echo "✅ Darwin cross-compilation environment ready!"
fi

echo "✅ Cross-compilation toolchains installed"
