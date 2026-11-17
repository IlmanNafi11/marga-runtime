#!/usr/bin/env bash
set -uo pipefail

# Matrix for require-go-workspace.sh: the guard must refuse a workspace-less
# build in CI and stay silent otherwise.
#
# Usage: ./require-go-workspace.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$SCRIPT_DIR/require-go-workspace.sh"

fail=0
check() { # <name> <expected-exit> <expected-stderr> <cwd> <CI value>
  local name="$1" want_exit="$2" want_err="$3" cwd="$4" ci="$5" got_exit err out
  out="$(cd "$cwd" && env -u CI ${ci:+CI="$ci"} bash "$GUARD" 2>/tmp/.rgw-err)"
  got_exit=$?
  err="$(cat /tmp/.rgw-err)"
  rm -f /tmp/.rgw-err
  if [ "$got_exit" != "$want_exit" ]; then
    echo "FAIL - $name: exit $got_exit, want $want_exit"
    fail=1
  elif [ "$err" != "$want_err" ]; then
    echo "FAIL - $name: stderr '$err', want '$want_err'"
    fail=1
  elif [ -n "$out" ]; then
    echo "FAIL - $name: wrote to stdout: '$out'"
    fail=1
  fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/with-workspace" "$WORK/without-workspace"
printf 'go 1.27.4\n' > "$WORK/with-workspace/go.work"

MSG="error: go.work is missing; builds must run against the local workspace"

check "go.work present, CI=1" 0 "" "$WORK/with-workspace" "1"
check "go.work absent, CI unset" 0 "" "$WORK/without-workspace" ""
check "go.work absent, CI=true" 1 "$MSG" "$WORK/without-workspace" "true"

[ "$fail" -eq 0 ] && echo "PASS"
exit "$fail"
