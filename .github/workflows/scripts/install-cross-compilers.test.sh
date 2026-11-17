#!/usr/bin/env bash
set -uo pipefail

# Pins install-cross-compilers.sh's platform argument: each target installs only
# what it needs, no name in /usr/local/bin shadows the real musl compiler, and an
# unknown target is refused before apt runs.
#
# Usage: ./install-cross-compilers.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$SCRIPT_DIR/install-cross-compilers.sh"

fail=0
fail_msg() { echo "FAIL - $1"; fail=1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"

# Stand-ins for apt and sudo: record argv and leave the system untouched.
cat > "$WORK/bin/sudo" <<'FAKE'
#!/usr/bin/env bash
exec "$@"
FAKE
cat > "$WORK/bin/apt-get" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$APT_LOG"
FAKE
chmod +x "$WORK/bin/sudo" "$WORK/bin/apt-get"
mkdir -p "$WORK/scripts"
cp "$SCRIPT" "$WORK/scripts/"
# The installer runs its sibling apt-sources-https.sh by path, so the stub goes
# next to the copy, not on PATH.
printf '#!/usr/bin/env bash\nexit 0\n' > "$WORK/scripts/apt-sources-https.sh"
chmod +x "$WORK/scripts/apt-sources-https.sh"

run_installer() { # <target>
  local target="$1"
  ( PATH="$WORK/bin:$PATH" APT_LOG="$WORK/apt.log" \
      bash "$WORK/scripts/install-cross-compilers.sh" "$target" ) > "$WORK/out" 2>&1
}

# 1. linux installs musl-tools and nothing else.
rm -f "$WORK/apt.log"
if run_installer linux; then :; else
  fail_msg "linux exited non-zero: $(cat "$WORK/out")"
fi
installed="$(tr '\n' ' ' < "$WORK/apt.log" 2>/dev/null || true)"
case "$installed" in
  *musl-tools*) ;;
  *) fail_msg "linux did not install musl-tools: $installed" ;;
esac
for unwanted in gcc-mingw-w64-x86-64 clang; do
  case "$installed" in
    *"$unwanted"*) fail_msg "linux installed $unwanted it does not need: $installed" ;;
  esac
done

# 2. windows installs mingw and not the mac toolchain.
rm -f "$WORK/apt.log"
if run_installer windows; then :; else
  fail_msg "windows exited non-zero: $(cat "$WORK/out")"
fi
installed="$(tr '\n' ' ' < "$WORK/apt.log" 2>/dev/null || true)"
case "$installed" in
  *gcc-mingw-w64-x86-64*) ;;
  *) fail_msg "windows did not install mingw: $installed" ;;
esac
case "$installed" in
  *musl-tools*) fail_msg "windows installed musl-tools: $installed" ;;
esac

# 3. An unknown target is a usage error (exit 2), before apt runs.
rm -f "$WORK/apt.log"
set +e
run_installer solaris
rc=$?
set -e
if [ "$rc" != 2 ]; then
  fail_msg "unknown target exited $rc, want 2"
fi
[ -s "$WORK/apt.log" ] && fail_msg "unknown target still ran apt"

# 4. No fabricated compiler names: /usr/local/bin must not shadow the real musl
#    binaries in /usr/bin. The script used to symlink those names to glibc gcc,
#    which silently turned the linux artifacts into glibc-linked builds.
if grep -q '/usr/local/bin/x86_64-linux-musl-gcc\|/usr/local/bin/aarch64-linux-musl-gcc' "$SCRIPT"; then
  fail_msg "script still writes a musl compiler name into /usr/local/bin"
fi

[ "$fail" -eq 0 ] && echo PASS
exit "$fail"
