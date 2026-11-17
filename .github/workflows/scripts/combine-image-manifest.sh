#!/usr/bin/env bash
set -euo pipefail

# Print, one line per repository, the argument list for
# `docker buildx imagetools create` that stitches the two per-architecture
# images into the multi-arch manifest list a release publishes.
#
# Source images are positional; `docker buildx imagetools create` rejects a
# `--amend` flag, so the per-arch tags carry the `-amd64`/`-arm64` suffix and the
# combined list is written over the plain version tags.
#
# Usage: combine-image-manifest.sh <version> <prerelease> <repo>...

if [ "$#" -lt 3 ]; then
  echo "usage: $0 <version> <prerelease> <repo>..." >&2
  exit 2
fi

version="${1#v}"
prerelease="$2"
shift 2

if [ -z "$version" ]; then
  echo "error: version must be non-empty" >&2
  exit 2
fi

case "$prerelease" in
  true | false) ;;
  *)
    echo "error: prerelease must be 'true' or 'false', got '$prerelease'" >&2
    exit 2
    ;;
esac

for repo in "$@"; do
  repo="${repo%/}"
  if [ -z "$repo" ]; then
    echo "error: repository must be non-empty" >&2
    exit 2
  fi

  args=(--tag "$repo:v${version}" --tag "$repo:${version}")
  if [ "$prerelease" = false ]; then
    args+=(--tag "$repo:latest")
  fi
  args+=("$repo:v${version}-amd64" "$repo:v${version}-arm64")

  printf '%s\n' "${args[*]}"
done
