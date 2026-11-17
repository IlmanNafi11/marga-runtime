#!/usr/bin/env bash
set -euo pipefail

# Run the module's unit tests, skipping the live-provider (llmtests) and MCP
# (mcptests) suites that need credentials or child-process fixtures. Both are
# excluded by import path and by a `*_test.go` that imports them, so a package
# that merely lives next to them is still tested.
#
# Usage: bash .github/workflows/scripts/run-core-unit-tests.sh [module-dir]

module="${1:-core}"

if [ ! -d "$module" ]; then
  echo "error: module directory '$module' does not exist" >&2
  exit 1
fi

cd "$module"

packages=()
while IFS=$'\t' read -r package directory; do
  case "$package" in
    */internal/llmtests|*/internal/mcptests)
      echo "Skipping live-suite package: $package"
      continue
      ;;
  esac
  skipped=""
  for suite in llmtests mcptests; do
    if find "$directory" -maxdepth 1 -name '*_test.go' -exec grep -lq "/internal/$suite" {} +; then
      echo "Skipping $suite-backed package: $package"
      skipped=1
      break
    fi
  done
  [ -n "$skipped" ] && continue
  packages+=("$package")
done < <(go list -f '{{.ImportPath}}{{"\t"}}{{.Dir}}' ./...)

if (( ${#packages[@]} == 0 )); then
  echo "error: no unit-test packages found" >&2
  exit 1
fi

go test -timeout 20m "${packages[@]}"
