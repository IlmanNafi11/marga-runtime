#!/usr/bin/env bash
set -uo pipefail

# Pins the argument list the manifest job feeds `docker buildx imagetools
# create`: sources are positional (the flag `--amend` does not exist and made
# every release fail with `unknown flag: --amend`), `latest` rides only on a
# stable version, and release.yml calls the helper instead of inlining the
# argument list again.
#
# Usage: ./combine-image-manifest.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/combine-image-manifest.sh"
WORKFLOW="$SCRIPT_DIR/../release.yml"

fail=0
fail_msg() {
  echo "FAIL - $1"
  fail=1
}

GHCR="ghcr.io/ilmannafi11/marga-runtime"
HUB="ilmannafi/marga-runtime"

# 1. A prerelease writes the version pair only.
out="$(bash "$SCRIPT" 0.0.1-spike true "$GHCR" "$HUB" 2>&1)" || fail_msg "prerelease exited non-zero: $out"
want="$GHCR:v0.0.1-spike"$'\n'"$GHCR:0.0.1-spike"$'\n'"$GHCR:v0.0.1-spike-amd64"$'\n'"$GHCR:v0.0.1-spike-arm64"

# Arguments are emitted space separated, so assert on the rendered command.
if ! grep -qx -- "--tag $GHCR:v0.0.1-spike --tag $GHCR:0.0.1-spike $GHCR:v0.0.1-spike-amd64 $GHCR:v0.0.1-spike-arm64" <<<"$out"; then
  fail_msg "prerelease ghcr line wrong: $(grep -m1 'ghcr' <<<"$out")"
fi
if ! grep -qx -- "--tag $HUB:v0.0.1-spike --tag $HUB:0.0.1-spike $HUB:v0.0.1-spike-amd64 $HUB:v0.0.1-spike-arm64" <<<"$out"; then
  fail_msg "prerelease dockerhub line wrong: $(grep -m1 'ilmannafi/' <<<"$out")"
fi
if grep -q 'latest' <<<"$out"; then
  fail_msg "prerelease emitted a latest tag"
fi
[ "$(wc -l <<<"$out")" -eq 2 ] || fail_msg "prerelease emitted $(wc -l <<<"$out") lines, want 2 (one per registry)"

# 2. A stable version carries latest.
out="$(bash "$SCRIPT" v2.6.0 false "$GHCR" 2>&1)" || fail_msg "stable exited non-zero: $out"
grep -qx -- "--tag $GHCR:v2.6.0 --tag $GHCR:2.6.0 --tag $GHCR:latest $GHCR:v2.6.0-amd64 $GHCR:v2.6.0-arm64" <<<"$out" ||
  fail_msg "stable line wrong: $out"

# 3. Both source images are the arch-suffixed tags, never the plain ones the
#    manifest list itself occupies.
if grep -qE 'v[^ ]+ (ghcr\.io|ilmannafi)/[^ ]*:v?0\.0\.1-spike( |$)' <<<"$(bash "$SCRIPT" 0.0.1-spike true "$GHCR")"; then
  fail_msg "a plain version tag was used as a source image"
fi

# 4. Usage errors exit 2 before printing anything.
set +e
out="$(bash "$SCRIPT" 0.0.1-spike maybe "$GHCR" 2>/dev/null)"; rc=$?
set -e
[ "$rc" = 2 ] || fail_msg "bad prerelease exited $rc, want 2"
[ -z "$out" ] || fail_msg "bad prerelease wrote to stdout: $out"

set +e
bash "$SCRIPT" 0.0.1-spike true >/dev/null 2>&1; rc=$?
set -e
[ "$rc" = 2 ] || fail_msg "no repository exited $rc, want 2"

# 5. The workflow must not reintroduce the unsupported flag, and must call the
#    helper rather than hand-rolling the argument list.
#    A prose mention in a comment is fine; a real argument is not.
if sed 's/#.*//' "$WORKFLOW" | grep -q -- '--amend'; then
  fail_msg "release.yml still passes --amend to imagetools create"
fi
if ! grep -q 'combine-image-manifest.sh' "$WORKFLOW"; then
  fail_msg "release.yml does not call combine-image-manifest.sh"
fi

[ "$fail" -eq 0 ] && echo PASS
exit "$fail"
