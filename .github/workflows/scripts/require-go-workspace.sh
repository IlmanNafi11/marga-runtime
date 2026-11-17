#!/usr/bin/env bash
set -euo pipefail

# Refuse a CI build that would resolve published github.com/maximhq/* modules
# instead of this repository's core/framework/plugins. Local builds without
# go.work stay allowed so an unpacked tree can still compile its own modules.
#
# Usage: bash .github/workflows/scripts/require-go-workspace.sh

if [ -f "go.work" ]; then
  exit 0
fi

if [ -n "${CI:-}" ]; then
  echo "error: go.work is missing; builds must run against the local workspace" >&2
  exit 1
fi
