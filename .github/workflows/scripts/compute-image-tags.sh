#!/usr/bin/env bash
set -euo pipefail

# Print the image tags one architecture push is published under: the arch pair,
# the version pair, the bare arch, and `latest` only for a stable version. One
# tag per line so a caller can feed the list straight to `docker build --tag`.
#
# Usage: bash .github/workflows/scripts/compute-image-tags.sh <version> <arch> <registry-prefix>

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <version> <arch> <registry-prefix>" >&2
  exit 2
fi

version="${1#v}"
arch="$2"
prefix="${3%/}"

if [ -z "$version" ] || [ -z "$arch" ] || [ -z "$prefix" ]; then
  echo "error: version, arch and registry prefix must be non-empty" >&2
  exit 2
fi

printf '%s\n' \
  "$prefix:v${version}-${arch}" \
  "$prefix:${version}-${arch}" \
  "$prefix:v${version}" \
  "$prefix:${version}" \
  "$prefix:${arch}"

case "$version" in
  *-*) ;;
  *) printf '%s\n' "$prefix:latest" ;;
esac
