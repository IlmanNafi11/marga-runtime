#!/usr/bin/env bash
set -euo pipefail

export GOTOOLCHAIN=auto

# Usage: bash setup-go-workspace.sh   (or `source` it; both work)
# Idempotent: an existing go.work is left exactly as it is.

if [ -f "go.work" ]; then
  echo "✅ Go workspace already exists, skipping init"
  exit 0
fi

echo "🔧 Setting up Go workspace..."
go work init
go work use ./core
go work use ./framework
go work use ./transports
go work use ./cli
# The Warp runner builds tests/cmd/seed/warpseed inside the workspace; its own go.sum
# does not cover framework's dependencies, so it cannot build with GOWORK=off.
go work use ./tests/cmd/seed
# Discover plugin modules instead of listing them, so a newly added plugin
# never needs a matching CI edit.
for plugin_dir in ./plugins/*/; do
  if [ -f "$plugin_dir/go.mod" ]; then
    go work use "$plugin_dir"
  fi
done
echo "✅ Go workspace initialized"
