#!/usr/bin/env bash
set -uo pipefail

# Pins the package selection of run-core-unit-tests.sh: llm/mcp suites are
# skipped by import path and by `*_test.go` imports, an empty selection fails,
# and the surviving packages reach `go test` with the documented timeout.
#
# Usage: ./run-core-unit-tests.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/run-core-unit-tests.sh"

fail=0
fail_msg() { echo "FAIL - $1"; fail=1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/bin" "$WORK/mod/plain" "$WORK/mod/internal/llmtests/tools" \
  "$WORK/mod/internal/mcptests/fixture" "$WORK/mod/llm-backed" "$WORK/empty"
printf 'package plain\n' > "$WORK/mod/plain/plain.go"
printf 'package plain\n' > "$WORK/mod/plain/plain_test.go"
printf 'package llm_backed\n' > "$WORK/mod/llm-backed/llm.go"
printf 'package llm_backed\nimport _ "example/core/internal/llmtests"\n' \
  > "$WORK/mod/llm-backed/llm_test.go"

# Fake go over a fixed four-package universe: `list` fails outside the fixture
# module (as the real go does), `ls-files` answers from the fixture tree, `test`
# records its argv.
cat > "$WORK/bin/go" <<'FAKE'
#!/usr/bin/env bash
case "$1" in
  list)
    if [ "$PWD" != "$FAKE_MOD" ]; then
      echo "pattern ./...: directory prefix . does not contain main module" >&2
      exit 1
    fi
    printf 'example/core/plain\t%s/plain\n' "$FAKE_MOD"
    printf 'example/core/internal/llmtests\t%s/internal/llmtests\n' "$FAKE_MOD"
    printf 'example/core/internal/mcptests\t%s/internal/mcptests\n' "$FAKE_MOD"
    printf 'example/core/llm-backed\t%s/llm-backed\n' "$FAKE_MOD"
    ;;
  ls-files)
    for file in "$FAKE_MOD"/$2; do
      [ -f "$file" ] && printf '%s\n' "${file#"$FAKE_MOD"/}"
    done
    ;;
  test)
    shift
    printf '%s\n' "$@" > "$FAKE_TEST_ARGS"
    ;;
  *)
    echo "unexpected go invocation: $*" >&2
    exit 9
    ;;
esac
FAKE
chmod +x "$WORK/bin/go"

run_script() { # <module> <args-file> <out-file>
  PATH="$WORK/bin:$PATH" FAKE_MOD="$WORK/mod" FAKE_TEST_ARGS="$2" \
    bash "$SCRIPT" "$1" > "$3" 2>&1
}

# 1. Selection: only the plain package reaches `go test`, in the documented form.
if run_script "$WORK/mod" "$WORK/args" "$WORK/out"; then :; else
  fail_msg "selection run exited non-zero: $(cat "$WORK/out")"
fi
if [ "$(tr '\n' ' ' < "$WORK/args" 2>/dev/null)" != "-timeout 20m example/core/plain " ]; then
  fail_msg "go test argv was '$(tr '\n' ' ' < "$WORK/args" 2>/dev/null)'"
fi

# 2. Empty selection is a hard failure with the documented message.
if run_script "$WORK/empty" "$WORK/args-empty" "$WORK/out-empty"; then
  fail_msg "empty selection exited 0"
else
  if ! grep -qx 'error: no unit-test packages found' "$WORK/out-empty"; then
    fail_msg "empty selection message was '$(cat "$WORK/out-empty")'"
  fi
fi

[ "$fail" -eq 0 ] && echo "PASS"
exit "$fail"
