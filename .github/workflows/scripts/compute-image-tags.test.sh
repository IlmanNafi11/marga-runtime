#!/usr/bin/env bash
set -uo pipefail

# Pins the tag list a release image is pushed with: one arch-specific pair per
# registry, the version pair, the bare arch, and `latest` only for a stable tag.
#
# Usage: ./compute-image-tags.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/compute-image-tags.sh"

fail=0
check() { # <name> <want> <args...>
  local name="$1" want="$2"
  shift 2
  local got
  if ! got="$(bash "$SCRIPT" "$@" 2>&1)"; then
    echo "FAIL - $name: exited non-zero: $got"
    fail=1
  elif [ "$got" != "$want" ]; then
    echo "FAIL - $name: got '$got'"
    fail=1
  fi
}

PREFIX="ghcr.io/ilmannafi11/marga-runtime"
STABLE="$PREFIX:v2.6.0-amd64
$PREFIX:2.6.0-amd64
$PREFIX:v2.6.0
$PREFIX:2.6.0
$PREFIX:amd64
$PREFIX:latest"
PRERELEASE="$PREFIX:v2.7.0-rc.1-arm64
$PREFIX:2.7.0-rc.1-arm64
$PREFIX:v2.7.0-rc.1
$PREFIX:2.7.0-rc.1
$PREFIX:arm64"

check "stable amd64" "$STABLE" 2.6.0 amd64 "$PREFIX"
check "prerelease arm64" "$PRERELEASE" 2.7.0-rc.1 arm64 "$PREFIX"

# Wrong argument count is a usage error, distinct from a failed input.
set +e
bash "$SCRIPT" 2.6.0 amd64 >/dev/null 2>&1
rc=$?
set -e
if [ "$rc" != 2 ]; then
  echo "FAIL - missing registry prefix exited $rc, want 2"
  fail=1
fi

[ "$fail" -eq 0 ] && echo "PASS"
exit "$fail"
