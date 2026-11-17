#!/usr/bin/env bash
set -uo pipefail

# Pins the release-binary contract: one artifact per platform, a sibling BSD
# sha256 with a bare filename, CGO on everywhere, static sqlite only on linux,
# and loud failures for an unsupported platform or a UI-less tree.
#
# Usage: ./build-release-binary.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/build-release-binary.sh"
REAL_GO="$(command -v go || true)"

fail=0
fail_msg() { echo "FAIL - $1"; fail=1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/bin" "$WORK/repo/transports/bifrost-http/ui" "$WORK/no-ui/transports/bifrost-http/ui" \
  "$WORK/repo/.github/workflows/scripts" "$WORK/no-ui/.github/workflows/scripts"
cp "$SCRIPT" "$WORK/repo/.github/workflows/scripts/"
cp "$SCRIPT" "$WORK/no-ui/.github/workflows/scripts/"
printf 'module example/fixture\n\ngo 1.21\n' > "$WORK/repo/go.mod"
printf 'package main\n\nfunc main() { println("ok") }\n' > "$WORK/repo/transports/bifrost-http/bifrost-http.go"
printf '<!doctype html>\n' > "$WORK/repo/transports/bifrost-http/ui/index.html"

# Fake go: records env and argv, and writes the `-o` target so the checksum
# step has a file to hash. With REAL_GO set it compiles the fixture instead, so
# the linux/amd64 case proves an artifact really runs, not just that a file
# appeared.
cat > "$WORK/bin/go" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$FAKE_ARGS"
printf 'CGO_ENABLED=%s\nGOOS=%s\nGOARCH=%s\nCC=%s\nCXX=%s\n' "${CGO_ENABLED:-}" "${GOOS:-}" "${GOARCH:-}" "${CC:-}" "${CXX:-}" > "$FAKE_ENV"
output=""
previous=""
for arg in "$@"; do
  [ "$previous" = "-o" ] && output="$arg"
  previous="$arg"
done
if [ -z "${REAL_GO:-}" ]; then
  printf 'binary\n' > "$output"
  exit 0
fi
args=()
skip_next=""
for arg in "$@"; do
  if [ -n "$skip_next" ]; then
    skip_next=""
    continue
  fi
  case "$arg" in
    -ldflags|-tags|-o) skip_next=1 ;;
    *) args+=("$arg") ;;
  esac
done
exec "$REAL_GO" build -trimpath -o "$output" ./
FAKE
chmod +x "$WORK/bin/go"

run_build() { # <repo> <outdir> <goos> <goarch> <args-file> <env-file> <err-file>
  ( cd "$1" && PATH="$WORK/bin:$PATH" REAL_GO="${FAKE_REAL_GO:-}" FAKE_ARGS="$5" FAKE_ENV="$6" \
      bash "$1/.github/workflows/scripts/build-release-binary.sh" "$3" "$4" 2.6.0 "$2" ) 2> "$7"
}

# 1. linux/amd64: static, buildid-stripped, hashed, and actually executable.
FAKE_REAL_GO="$REAL_GO"
if run_build "$WORK/repo" "$WORK/out-linux" linux amd64 "$WORK/a1" "$WORK/e1" "$WORK/err1"; then :; else
  fail_msg "linux/amd64 exited non-zero: $(cat "$WORK/err1")"
fi
FAKE_REAL_GO=""
artifact="$WORK/out-linux/marga-runtime-linux-amd64"
[ -f "$artifact" ] || fail_msg "linux/amd64 artifact missing"
[ -f "$artifact.sha256" ] || fail_msg "linux/amd64 checksum missing"
if ! grep -Eqx '[0-9a-f]{64}  marga-runtime-linux-amd64' "$artifact.sha256"; then
  fail_msg "linux/amd64 checksum is not a BSD line with a bare filename: $(cat "$artifact.sha256" 2>/dev/null)"
fi
if [ "$(tr '\n' ' ' < "$WORK/e1")" != "CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=x86_64-linux-musl-gcc CXX=x86_64-linux-musl-g++ " ]; then
  fail_msg "linux/amd64 env was '$(tr '\n' ' ' < "$WORK/e1")'"
fi
args1="$(tr '\n' ' ' < "$WORK/a1")"
case "$args1" in
  *"sqlite_static"*) ;;
  *) fail_msg "linux/amd64 did not tag sqlite_static: $args1" ;;
esac
case "$args1" in
  *"-buildid="*) ;;
  *) fail_msg "linux/amd64 did not strip the build id: $args1" ;;
esac
case "$args1" in
  *"-extldflags"*"-static"*) ;;
  *) fail_msg "linux/amd64 did not link statically: $args1" ;;
esac
if [ -n "$REAL_GO" ] && [ -x "$artifact" ]; then
  if [ "$("$artifact" 2>&1)" != "ok" ]; then
    fail_msg "linux/amd64 artifact did not run"
  fi
fi

# 2. windows/amd64: .exe, no static sqlite, no build-id flag.
if run_build "$WORK/repo" "$WORK/out-win" windows amd64 "$WORK/a2" "$WORK/e2" "$WORK/err2"; then :; else
  fail_msg "windows/amd64 exited non-zero: $(cat "$WORK/err2")"
fi
[ -f "$WORK/out-win/marga-runtime-windows-amd64.exe" ] || fail_msg "windows/amd64 artifact missing"
args2="$(tr '\n' ' ' < "$WORK/a2")"
for banned in sqlite_static buildid; do
  case "$args2" in
    *"$banned"*) fail_msg "windows/amd64 carried '$banned': $args2" ;;
  esac
done
if [ "$(grep '^CGO_ENABLED=' "$WORK/e2")" != "CGO_ENABLED=1" ]; then
  fail_msg "windows/amd64 built without CGO: $(cat "$WORK/e2")"
fi
# The runner's native gcc cannot build runtime/cgo for windows -- it fails on
# `windows.h: No such file or directory` -- so the mingw cross compiler must be
# selected here, exactly as the darwin branches select o64/oa64-clang.
if ! grep -qx 'CC=x86_64-w64-mingw32-gcc' "$WORK/e2"; then
  fail_msg "windows/amd64 built without the mingw cross compiler: $(cat "$WORK/e2")"
fi

# 3. darwin/arm64: plain artifact, no static sqlite.
if run_build "$WORK/repo" "$WORK/out-mac" darwin arm64 "$WORK/a3" "$WORK/e3" "$WORK/err3"; then :; else
  fail_msg "darwin/arm64 exited non-zero: $(cat "$WORK/err3")"
fi
[ -f "$WORK/out-mac/marga-runtime-darwin-arm64" ] || fail_msg "darwin/arm64 artifact missing"
case "$(tr '\n' ' ' < "$WORK/a3")" in
  *sqlite_static*) fail_msg "darwin/arm64 carried sqlite_static" ;;
esac
# 3b. A relative outdir is resolved once, from the repo root. Resolving it
# again after `cd transports/bifrost-http` made `go build -o` write
# transports/bifrost-http/dist-spike/... while the checksum and the existence
# check looked for <repo>/dist-spike/..., so the spike job reported
# "go build produced no dist-spike/..." after a build that had succeeded.
if run_build "$WORK/repo" dist-spike darwin arm64 "$WORK/a3b" "$WORK/e3b" "$WORK/err3b"; then :; else
  fail_msg "relative outdir exited non-zero: $(cat "$WORK/err3b")"
fi
[ -f "$WORK/repo/dist-spike/marga-runtime-darwin-arm64" ] || fail_msg "relative outdir artifact not under the repo root"
[ -f "$WORK/repo/dist-spike/marga-runtime-darwin-arm64.sha256" ] || fail_msg "relative outdir checksum missing"
[ -d "$WORK/repo/transports/bifrost-http/dist-spike" ] && fail_msg "relative outdir escaped into transports/bifrost-http"

# 4. A platform outside the matrix is refused before any build starts.
set +e
run_build "$WORK/repo" "$WORK/out-bad" linux 386 "$WORK/a4" "$WORK/e4" "$WORK/err4"
rc=$?
set -e
if [ "$rc" != 2 ]; then
  fail_msg "unsupported platform exited $rc, want 2"
fi
[ -f "$WORK/a4" ] && fail_msg "unsupported platform still invoked go"

# 5. A tree without the embedded UI fails with the actionable message.
set +e
run_build "$WORK/no-ui" "$WORK/out-noui" linux amd64 "$WORK/a5" "$WORK/e5" "$WORK/err5"
rc=$?
set -e
if [ "$rc" != 1 ]; then
  fail_msg "UI-less tree exited $rc, want 1"
elif ! grep -q "run 'make build-ui' first" "$WORK/err5"; then
  fail_msg "UI-less tree message was '$(cat "$WORK/err5")'"
fi

[ "$fail" -eq 0 ] && echo "PASS"
exit "$fail"
