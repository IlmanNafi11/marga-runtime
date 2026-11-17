# Marga Runtime Release Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace bifrost's 23-workflow release apparatus with three workflows that build a single static binary (5 platforms) and a multi-arch Docker image published to GHCR + Docker Hub, triggered by a Git tag.

**Architecture:** Build always from the local Go workspace (`go.work`) so the fork's own `core`/`framework`/`plugins` sources are compiled instead of the published `maximhq` modules. A Git tag `v*` is the only version source. GitHub Actions does the cross-compiling with the repo's existing cross-compiler installer; Docker images are built natively per architecture on `ubuntu-24.04` / `ubuntu-24.04-arm`.

**Tech Stack:** Go 1.27.x (CGO, `sqlite_static`), Node (`.nvmrc`), GitHub Actions, `docker/build-push-action` + buildx, Alpine/musl static binaries.

**Spec:** `docs/superpowers/specs/2026-11-17-marga-runtime-release-design.md`

## Global Constraints

- Go version: whatever `go.work`/`go.mod` declare (currently `go 1.27.4` / `go 1.27.3`); do not hardcode a different floor in workflows.
- Node version: from `.nvmrc`.
- `CGO_ENABLED=1` for every build; `CGO_ENABLED=0` is not an option (sqlite3).
- Linux builds: `-tags "netgo,osusergo,sqlite_static"`, `-ldflags "-s -w -buildid= -extldflags '-static'"`.
- macOS/Windows builds: no `sqlite_static`, no `-extldflags '-static'`; `-ldflags "-s -w -buildid="`.
- `GOWORK` must be active (i.e. `go.work` present) for every build. Never `GOWORK=off` except where explicitly stated.
- Version: tag name minus leading `v`; tag must match `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`.
- Artifact name: `marga-runtime-<goos>-<goarch>` (`.exe` for windows), plus the same name + `.sha256`.
- Image name: `marga-runtime`; GHCR ref `ghcr.io/<owner-lowercased>/marga-runtime`; Docker Hub ref `${DOCKERHUB_USERNAME}/marga-runtime`.
- Image tags per release: `vX.Y.Z`, `X.Y.Z`, `-amd64`, `-arm64`; `latest` only for a stable version (no `-` suffix).
- Required repo config: variable `DOCKERHUB_USERNAME`, secret `DOCKERHUB_TOKEN`. `GITHUB_TOKEN` otherwise.
- Branch: `dev`. No `main`.
- Platform matrix: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`.

## Status (2026-11-17)

Tasks 1–10 are implemented and verified locally; every step box is checked. Local evidence: the five `*.test.sh` suites pass, `actionlint` v1.7.12 is clean on `ci.yml`/`release.yml`/`full-tests.yml`, `CI=1 make build` produces `tmp/bifrost-http` and exits non-zero once `go.work` is moved aside, the image boots with `v2.6.0` on `/health` and HTML on `/`, and the linux/amd64 release artifact is a static musl binary that boots with the matching banner.

`spike-cross` is green on the real runner (runs 44044080277 and 44068936825): `darwin/amd64`, `darwin/arm64` and `windows/amd64` each built, checksummed and uploaded an artifact, so the cross toolchains work. Getting there needed three fixes the local contract tests did not cover, all pinned by new cases now: `setup-go` read the gitignored `go.work`; `install-cross-compilers.sh` tripped over the EC2 runner's regional `apt-mirrors.txt`; and `build-release-binary.sh` neither exported the per-platform `CC` nor resolved a relative outdir before its `cd`.

`ci.yml` is green end to end (run 44066288198, 17m). The first attempt failed on 44 `framework/vectorstore` tests and on `plugins/semanticcache`, because those suites gate their integration paths on `testing.Short()`, not on `CI`; the light tier now passes `-short` in `ci.yml` and in `verify`, and the Redis config/TLS-shape tests assert on the client builder (`RedisConfig.newClient()`) instead of a store that pings. `full-tests.yml` still runs without `-short`.

Outstanding, operator-only: the `v0.0.1-spike` rehearsal tag, which is the only end-to-end proof of the publish, image, manifest and release-asset steps.

## Review Focus

1. **A build that resolves published `maximhq` modules instead of the local tree** — a build that goes green while compiling someone else's code. Pinned by Task 1's guard test.
2. **A binary that boots but serves no UI** — `//go:embed all:ui` fails at compile time if `transports/bifrost-http/ui` is empty, but a stale/absent directory can also produce a binary whose `/` is a 404. Pinned by Task 2's container smoke test and Task 5's binary run.
3. **Wrong version baked into the artifact or into the release name** — operator cannot tell which build runs. Pinned by Task 5's banner check.
4. **`latest` moved by a prerelease tag** — consumers silently receive an RC. Pinned by Task 4's tag-computation test.
5. **Deleting a script still referenced by `make` targets** — a local workflow dies with "No such file". Pinned by Task 9's reference sweep.

---

### Task 1: Workspace guard + `make build` uses the workspace by default

**Files:**
- Create: `.github/workflows/scripts/require-go-workspace.sh`
- Create: `.github/workflows/scripts/require-go-workspace.test.sh`
- Modify: `Makefile:388-420` (the three `GOWORK=off` occurrences in `build`), `Makefile:426-430` (`build-cli`)

**Interfaces:**
- Produces: `require-go-workspace.sh` — exits 1 with `error: go.work is missing; builds must run against the local workspace` when `go.work` is absent *and* `CI` is non-empty; exits 0 otherwise. Sourced/called from `Makefile` before every `go build`.

- [x] **Step 1: Write the failing test**

```bash
#!/usr/bin/env bash
# .github/workflows/scripts/require-go-workspace.test.sh
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="${SCRIPT_DIR}/require-go-workspace.sh"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

expect_ok()   { ( cd "$1"; shift; env "$@" bash "$GUARD" ) >/dev/null 2>&1; }
expect_fail() { ( cd "$1"; shift; env "$@" bash "$GUARD" ) >/dev/null 2>&1 && return 1 || return 0; }

mkdir -p "$WORK/with" "$WORK/without"
: > "$WORK/with/go.work"

fail=0
expect_ok   "$WORK/with"    CI=1        || { echo "FAIL: go.work present + CI should pass"; fail=1; }
expect_ok   "$WORK/without" CI=         || { echo "FAIL: go.work absent + no CI should pass"; fail=1; }
expect_fail "$WORK/without" CI=true     || { echo "FAIL: go.work absent + CI should fail"; fail=1; }
exit $fail
```

- [x] **Step 2: Run it to verify it fails**

Run: `bash .github/workflows/scripts/require-go-workspace.test.sh`
Expected: exits non-zero, both `FAIL:` lines appear (the script does not exist yet, so `bash` errors and every `expect_*` misreports).

- [x] **Step 3: Implement the guard**

```bash
#!/usr/bin/env bash
# .github/workflows/scripts/require-go-workspace.sh
# Every build must compile the fork's own core/framework/plugins. With GOWORK=off
# (or no go.work) Go resolves the published github.com/maximhq/bifrost modules
# instead, which produces a green build of code that is not in this repository.
set -euo pipefail
if [ ! -f go.work ] && [ -n "${CI:-}" ]; then
  echo "error: go.work is missing; builds must run against the local workspace" >&2
  echo "       run 'source .github/workflows/scripts/setup-go-workspace.sh' first" >&2
  exit 1
fi
exit 0
```

- [x] **Step 4: Run the test to verify it passes**

Run: `bash .github/workflows/scripts/require-go-workspace.test.sh`
Expected: exit 0, no output.

- [x] **Step 5: Wire the guard into the Makefile and drop `GOWORK=off` from builds**

In `build` (lines ~388-420) and `build-cli`:
- Insert `@bash .github/workflows/scripts/require-go-workspace.sh` as the first line of each recipe body.
- Replace `$(if $(LOCAL),,GOWORK=off)` with nothing in all three `go build` invocations and in `build-cli` (`@cd cli && go build …`). Remove the `LOCAL` banner branch in `build` that only exists to announce `GOWORK=off`, keeping one banner line.
- In `_build-with-docker` (lines ~440-470) delete both `$(if $(LOCAL),,-e GOWORK=off)` arguments; the container must see the workspace copy.

- [x] **Step 6: Verify the Makefile path end to end**

Run: `bash .github/workflows/scripts/setup-go-workspace.sh && CI=1 make build-ui && CI=1 make build`
Expected: build succeeds and `tmp/bifrost-http` exists.
Then: `mv go.work /tmp/go.work.bak && CI=1 bash .github/workflows/scripts/require-go-workspace.sh; echo "exit=$?"; mv /tmp/go.work.bak go.work`
Expected: `exit=1`.

- [x] **Step 7: Commit**

```bash
git add Makefile .github/workflows/scripts/require-go-workspace.sh .github/workflows/scripts/require-go-workspace.test.sh
git commit -m "build: compile against the local workspace and refuse GOWORK=off builds"
```

---

### Task 2: Root Dockerfile built from the workspace

**Files:**
- Create: `Dockerfile` (root)
- Modify: `.dockerignore` (remove the `go.work` / `go.work.sum` exclusions)
- Modify: `Makefile:482-488` (`docker-image`)

**Interfaces:**
- Consumes: `go.work` creation pattern from `.github/workflows/scripts/setup-go-workspace.sh`.
- Produces: image exposing `8080`, entrypoint `/app/docker-entrypoint.sh`, data dir `/app/data`, healthcheck `GET /health`. Build context is the repo root; `docker build --build-arg VERSION=2.6.0 .`

- [x] **Step 1: Write the Dockerfile**

Start from `transports/Dockerfile.local` verbatim, then apply exactly these changes:
- UI stage: unchanged (`COPY ui/package*.json ./`, `npm ci`, `COPY ui/ ./`, `npm run build-enterprise`).
- Go stage: unchanged copies (`core/`, `framework/`, `plugins/`, `transports/`) and unchanged `go work init` / `go work use` loop.
- Keep `ARG VERSION=unknown` and `-X main.Version=v${VERSION}`.
- Runtime stage: unchanged (alpine 3.23.7, `musl libgcc ca-certificates zlib`, `adduser`, `USER 1000:0`, `VOLUME`, `EXPOSE 8080`, `HEALTHCHECK`, `ENTRYPOINT ["/app/docker-entrypoint.sh"]`).
- Remove the leading two-space indentation the original file has on every Dockerfile directive (`  FROM` → `FROM`); Docker tolerates it, but the repo's new root file should not be indented.

- [x] **Step 2: Let the workspace into the build context**

Edit `.dockerignore`: delete the `# Go workspaces (local only)` block (`go.work`, `go.work.sum`). Leave `tests/`, `docs/`, `*.md`, `tmp/` excluded.

- [x] **Step 3: Point `make docker-image` at the new file**

In `Makefile:482-488`: `$(eval DOCKERFILE=$(if $(LOCAL),transports/Dockerfile.local,transports/Dockerfile))` becomes `DOCKERFILE=Dockerfile`; image names become `marga-runtime`, `marga-runtime:$(GIT_SHA)`, `marga-runtime:latest`; the help comment loses the `LOCAL=1` note.

- [x] **Step 4: Build and smoke-test the image**

Run:
```bash
docker build --build-arg VERSION=2.6.0 -t marga-runtime:smoke .
docker run -d --rm -p 18080:8080 -e APP_HOST=0.0.0.0 --name marga-smoke marga-runtime:smoke
sleep 5; curl -sf http://127.0.0.1:18080/health && echo OK
curl -s http://127.0.0.1:18080/ | head -c 200; echo
docker logs marga-smoke 2>&1 | grep -i "v2.6.0" | head -2
docker rm -f marga-smoke
```
Expected: `/health` returns OK, `/` returns HTML (not a 404 / empty body), log banner contains `v2.6.0`.

- [x] **Step 5: Commit**

```bash
git add Dockerfile .dockerignore Makefile
git commit -m "build: add a root Dockerfile that builds from the local workspace"
```

---

### Task 3: `run-core-unit-tests.sh`

**Files:**
- Create: `.github/workflows/scripts/run-core-unit-tests.sh`
- Create: `.github/workflows/scripts/run-core-unit-tests.test.sh`

**Interfaces:**
- Produces: `run-core-unit-tests.sh [module-dir]` (default `core`). Selects packages with `go list -f '{{.ImportPath}}{{"\t"}}{{.Dir}}' ./...`, skipping any package whose import path ends in `/internal/llmtests` or `/internal/mcptests`, and any package whose `*_test.go` imports `/internal/llmtests` or `/internal/mcptests`; runs `go test -timeout 20m <packages...>`; exits 1 with `error: no unit-test packages found` if the list is empty.

- [x] **Step 1: Write the failing test**

The test puts a fake `go` on `PATH` that prints a fixed package list and records its arguments:

```bash
#!/usr/bin/env bash
# .github/workflows/scripts/run-core-unit-tests.test.sh
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="${SCRIPT_DIR}/run-core-unit-tests.sh"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin" "$WORK/core/plain" "$WORK/core/llm-backed" "$WORK/core/internal/llmtests" "$WORK/core/internal/mcptests"
: > "$WORK/core/plain/plain_test.go"
printf 'import _ "x/internal/llmtests"\n' > "$WORK/core/llm-backed/llm_test.go"
cat > "$WORK/bin/go" <<EOF
#!/usr/bin/env bash
if [ "\$1" = list ]; then
  printf 'example/core/plain\t$WORK/core/plain\n'
  printf 'example/core/llm-backed\t$WORK/core/llm-backed\n'
  printf 'example/core/internal/llmtests\t$WORK/core/internal/llmtests\n'
  printf 'example/core/internal/mcptests\t$WORK/core/internal/mcptests\n'
  exit 0
fi
printf '%s\n' "\$*" >> "$WORK/go-test.args"
EOF
chmod +x "$WORK/bin/go"
PATH="$WORK/bin:$PATH" bash "$RUNNER" "$WORK/core" || { echo "FAIL: runner exited non-zero"; exit 1; }
args="$(cat "$WORK/go-test.args")"
[ "$args" = "test -timeout 20m example/core/plain" ] || { echo "FAIL: unexpected go test args: $args"; exit 1; }
echo "PASS"
```

- [x] **Step 2: Run it to verify it fails**

Run: `bash .github/workflows/scripts/run-core-unit-tests.test.sh`
Expected: non-zero exit (`run-core-unit-tests.sh: No such file or directory`).

- [x] **Step 3: Implement the runner**

Port the selection loop from `release-pipeline.yml:1034-1060` into a script, adding `mcptests` to both skip checks (`*/internal/mcptests` and the `grep -lq` pattern `/internal/mcptests`), keeping the `llmtests` checks as they are, and keeping the empty-list error.

- [x] **Step 4: Run the test to verify it passes**

Run: `bash .github/workflows/scripts/run-core-unit-tests.test.sh`
Expected: `PASS`.

- [x] **Step 5: Run it for real against the repo**

Run: `source .github/workflows/scripts/setup-go-workspace.sh && bash .github/workflows/scripts/run-core-unit-tests.sh core 2>&1 | tail -20`
Expected: package list printed (skipped ones logged), `ok`/`no test files` lines, exit 0. If a package fails for a missing service, that package belongs to the skip list — record which and stop; do not add Docker here.

- [x] **Step 6: Commit**

```bash
git add .github/workflows/scripts/run-core-unit-tests.sh .github/workflows/scripts/run-core-unit-tests.test.sh
git commit -m "ci: extract the lightweight core unit-test selection into a script"
```

---

### Task 4: `compute-image-tags.sh`

**Files:**
- Create: `.github/workflows/scripts/compute-image-tags.sh`
- Create: `.github/workflows/scripts/compute-image-tags.test.sh`

**Interfaces:**
- Produces: `compute-image-tags.sh <version> <arch> <registry-prefix>` printing one tag per line, newline-separated, to stdout, and setting no env. For `2.6.0 amd64 ghcr.io/x/marga-runtime`:
  `ghcr.io/x/marga-runtime:v2.6.0-amd64`, `:2.6.0-amd64`, `:v2.6.0`, `:2.6.0`, `:amd64`, `:latest`.
  For `2.7.0-rc.1 amd64 …` the same list **without** `:latest`.

- [x] **Step 1: Write the failing test**

```bash
#!/usr/bin/env bash
# .github/workflows/scripts/compute-image-tags.test.sh
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
S="${SCRIPT_DIR}/compute-image-tags.sh"
P="ghcr.io/x/marga-runtime"

stable="$(bash "$S" 2.6.0 amd64 "$P")"
grep -qx "$P:v2.6.0"       <<<"$stable" || { echo "FAIL: missing v2.6.0"; exit 1; }
grep -qx "$P:v2.6.0-amd64" <<<"$stable" || { echo "FAIL: missing v2.6.0-amd64"; exit 1; }
grep -qx "$P:amd64"        <<<"$stable" || { echo "FAIL: missing amd64"; exit 1; }
grep -qx "$P:latest"       <<<"$stable" || { echo "FAIL: stable must move latest"; exit 1; }

pre="$(bash "$S" 2.7.0-rc.1 arm64 "$P")"
grep -qx "$P:latest" <<<"$pre" && { echo "FAIL: prerelease moved latest"; exit 1; }
grep -qx "$P:v2.7.0-rc.1-arm64" <<<"$pre" || { echo "FAIL: missing prerelease per-arch tag"; exit 1; }
echo PASS
```

- [x] **Step 2: Run it to verify it fails**

Run: `bash .github/workflows/scripts/compute-image-tags.test.sh`
Expected: non-zero (script missing).

- [x] **Step 3: Implement the script**

Three args required; `:latest` is emitted only when `$1` contains no `-`; `-amd64`/`-arm64` are emitted both on the release version and on the bare arch tag. Exit 2 on wrong arg count.

- [x] **Step 4: Run the test to verify it passes**

Run: `bash .github/workflows/scripts/compute-image-tags.test.sh`
Expected: `PASS`.

- [x] **Step 5: Commit**

```bash
git add .github/workflows/scripts/compute-image-tags.sh .github/workflows/scripts/compute-image-tags.test.sh
git commit -m "ci: compute image tags so prereleases never move latest"
```

---

### Task 5: `build-release-binary.sh`

**Files:**
- Create: `.github/workflows/scripts/build-release-binary.sh`
- Create: `.github/workflows/scripts/build-release-binary.test.sh`

**Interfaces:**
- Produces: `build-release-binary.sh <goos> <goarch> <version> [outdir]` (outdir default `dist`). Writes `<outdir>/marga-runtime-<goos>-<goarch>[.exe]` and the sibling `.sha256` (BSD `shasum -a 256` output format: `<hash>  <filename>`). Exits 2 on a boundary not in the platform matrix.

- [x] **Step 1: Write the failing test**

Fake `go` records the last invocation's environment and args:

```bash
#!/usr/bin/env bash
# .github/workflows/scripts/build-release-binary.test.sh
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
B="${SCRIPT_DIR}/build-release-binary.sh"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
cat > "$WORK/bin/go" <<EOF
#!/usr/bin/env bash
env | grep -E '^(GOOS|GOARCH|CC|CGO_ENABLED)=' | sort > "$WORK/env.txt"
printf '%s\n' "\$*" > "$WORK/args.txt"
out=""; prev=""
for a in "\$@"; do [ "\$prev" = "-o" ] && out="\$a"; prev="\$a"; done
: > "\$out"
EOF
chmod +x "$WORK/bin/go"

PATH="$WORK/bin:$PATH" bash "$B" linux amd64 2.6.0 "$WORK/dist" || { echo "FAIL: linux run"; exit 1; }
test -x "$WORK/dist/marga-runtime-linux-amd64" || { echo "FAIL: linux artifact missing"; exit 1; }
grep -q '^CGO_ENABLED=1$' "$WORK/env.txt" || { echo "FAIL: CGO off"; exit 1; }
grep -q "sqlite_static" "$WORK/args.txt" || { echo "FAIL: linux needs sqlite_static"; exit 1; }
grep -q -- "-buildid=" "$WORK/args.txt" || { echo "FAIL: reproducible flags missing"; exit 1; }

PATH="$WORK/bin:$PATH" bash "$B" windows amd64 2.6.0 "$WORK/dist" || { echo "FAIL: windows run"; exit 1; }
test -f "$WORK/dist/marga-runtime-windows-amd64.exe" || { echo "FAIL: windows artifact missing"; exit 1; }
grep -q "sqlite_static" "$WORK/args.txt" && { echo "FAIL: windows must not use sqlite_static"; exit 1; }

bash "$B" linux mips 2.6.0 "$WORK/dist" 2>/dev/null && { echo "FAIL: unsupported boundary accepted"; exit 1; }
echo PASS
```

- [x] **Step 2: Run it to verify it fails**

Run: `bash .github/workflows/scripts/build-release-binary.test.sh`
Expected: non-zero (script missing).

- [x] **Step 3: Implement the script**

Adapt the per-platform branches of `.github/workflows/scripts/build-executables.sh:80-130`:
- `linux/amd64`: `CC=x86_64-linux-musl-gcc CXX=x86_64-linux-musl-g++`, tags `netgo,osusergo,sqlite_static`, ldflags `-s -w -buildid= -extldflags '-static' -X main.Version=v<version>`.
- `linux/arm64`: same with `aarch64-linux-musl-gcc/g++`.
- `darwin/amd64`: `CC=o64-clang CXX=o64-clang++`, ldflags `-s -w -buildid= -X main.Version=v<version>`, `-trimpath`.
- `darwin/arm64`: `CC=oa64-clang CXX=oa64-clang++`.
- `windows/amd64`: `CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++`.
- `cd transports/bifrost-http`, `go build … -o <abs outdir>/marga-runtime-<goos>-<goarch>[.exe] .`, then `shasum -a 256` into the sibling `.sha256` (compute inside `outdir` so the file name in the checksum is bare).
- Any other `goos/goarch`: `echo "unsupported platform: $goos/$goarch" >&2; exit 2`.

- Before building, refuse an empty embedded UI (a UI-less binary compiles only when the directory is absent, and `//go:embed` then fails the build with a less obvious message):
  `[ -n "$(ls -A transports/bifrost-http/ui 2>/dev/null)" ] || { echo "error: transports/bifrost-http/ui is empty; run 'make build-ui' first" >&2; exit 1; }`

- [x] **Step 4: Run the test to verify it passes**

Run: `bash .github/workflows/scripts/build-release-binary.test.sh`
Expected: `PASS`.

- [x] **Step 5: Build one real artifact locally**

Run: `bash .github/workflows/scripts/setup-go-workspace.sh && make build-ui && bash .github/workflows/scripts/build-release-binary.sh linux amd64 2.6.0 /tmp/marga-dist && /tmp/marga-dist/marga-runtime-linux-amd64 -port 18081 -app-dir /tmp/marga-data & sleep 5; curl -sf http://127.0.0.1:18081/health && curl -s http://127.0.0.1:18081/ | head -c 120; kill %1`
Expected: `/health` OK, `/` returns HTML, and the startup banner carries `v2.6.0`.

- [x] **Step 6: Commit**

```bash
git add .github/workflows/scripts/build-release-binary.sh .github/workflows/scripts/build-release-binary.test.sh
git commit -m "build: single release-binary script for all five platforms"
```

---

### Task 6: `ci.yml`

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `require-go-workspace.sh`, `run-core-unit-tests.sh` (Tasks 1, 3).
- Produces: workflow name `CI`; a single job `build`. `release.yml` reuses the same script set.

- [x] **Step 1: Write the workflow**

```yaml
name: CI
on:
  pull_request:
  push:
    branches: [dev]
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
permissions:
  contents: read
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod          # toolchain resolution comes from go.mod/go.work
          cache-dependency-path: |
            core/go.sum
            framework/go.sum
            transports/go.sum
            plugins/*/go.sum
      - uses: actions/setup-node@v4
        with:
          node-version-file: .nvmrc
          cache: npm
          cache-dependency-path: ui/package-lock.json
      - name: Go workspace
        run: source .github/workflows/scripts/setup-go-workspace.sh
      - name: Build UI + binary
        run: |
          make build-ui
          make build
      - uses: actions/upload-artifact@v7
        with:
          name: bifrost-http-linux-amd64
          path: tmp/bifrost-http
          retention-days: 3
      - name: go vet
        run: |
          for m in core framework transports plugins/*/; do
            ( cd "$m" && go vet ./... )
          done
      - name: Core unit tests
        run: bash .github/workflows/scripts/run-core-unit-tests.sh core
      - name: Framework/transport/plugin unit tests
        run: |
          # -short is what makes "no service containers" true: the service-backed
          # tests then skip themselves instead of failing on a refused port.
          for m in framework transports plugins/*/; do
            echo "== $m"
            ( cd "$m" && env -u CI go test -short -timeout 20m ./... )
          done
      - name: UI i18n
        working-directory: ui
        run: npm ci --ignore-scripts --no-audit --no-fund && npm run i18n:check
```

- [x] **Step 2: Lint the workflow**

Run: `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yml`
Expected: no output, exit 0.

- [x] **Step 3: Dry-run the same steps locally**

Run: `source .github/workflows/scripts/setup-go-workspace.sh && make build-ui && make build && bash .github/workflows/scripts/run-core-unit-tests.sh core && (cd framework && CI="" go test -timeout 20m ./... 2>&1 | tail -5)`
Expected: all pass; record the wall-clock so the plan's "6–8 minutes" claim can be checked against the first real run.

- [x] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: one lightweight build+unit-test workflow on dev"
```

---

### Task 7: `release.yml` and the cross-compiler spike

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `build-release-binary.sh`, `compute-image-tags.sh`, `run-core-unit-tests.sh`, `install-cross-compilers.sh`.
- Produces: GitHub Release `vX.Y.Z` with 10 files (5 binaries + 5 checksums) and images in both registries. Pre-release flag follows a `-` suffix in the tag.

- [x] **Step 1: Add the spike job first**

Add to `release.yml` a `workflow_dispatch`-only trigger in addition to `push: tags: ['v*']`, and a job `spike-cross` with `if: github.event_name == 'workflow_dispatch'` that runs `bash .github/workflows/scripts/install-cross-compilers.sh`, `make build-ui`, then for each of `darwin amd64`, `darwin arm64`, `windows amd64` runs `build-release-binary.sh <goos> <goarch> 0.0.0 dist-spike` and uploads the artifacts. All other jobs get `if: github.event_name == 'push'`.

- [x] **Step 2: Push the branch and run the spike**

Run: `git push origin HEAD:dev` then trigger the workflow from the Actions UI (`Run workflow`).
Expected: `spike-cross` green and three artifacts present. If a toolchain is missing, capture the failing command and stop — the fallback (Linux-only) is a user decision, not a builder decision.

- [x] **Step 3: Write the release jobs**

| Job | Steps (exact) |
|---|---|
| `meta` | `VERSION="${GITHUB_REF_NAME#v}"`; validate against `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`; output `version` and `prerelease` (`true` when the tag contains `-`). |
| `verify` | checkout, setup Go, `source setup-go-workspace.sh`, `run-core-unit-tests.sh core`, `CI="" go test -short ./...` in `framework`/`transports`/`plugins/*`. |
| `binaries` | `needs: [meta, verify]`; matrix `{os, arch, runner}`: `linux/amd64 → ubuntu-24.04`, `linux/arm64 → ubuntu-24.04-arm`, `darwin/amd64, darwin/arm64, windows/amd64 → ubuntu-24.04`; steps: checkout, setup Go, setup Node, `install-cross-compilers.sh`, `make build-ui`, `build-release-binary.sh`, upload artifact `binary-<os>-<arch>`. |
| `publish-release` | `needs: [meta, binaries]`; download all artifacts; `softprops/action-gh-release@v2` with `tag_name: ${{ github.ref_name }}`, `prerelease: ${{ needs.meta.outputs.prerelease == 'true' }}`, `generate_release_notes: true`, `files: dist/**`. |
| `image` | `needs: [meta, verify]`; matrix `{arch, runner}` = `amd64/ubuntu-24.04`, `arm64/ubuntu-24.04-arm`; steps: checkout, setup-buildx, login GHCR (`GITHUB_TOKEN`) and Docker Hub (`vars.DOCKERHUB_USERNAME` / `secrets.DOCKERHUB_TOKEN`), fail fast if `vars.DOCKERHUB_USERNAME` is empty, `docker/build-push-action` pushing the tag list from `compute-image-tags.sh` for both registries, `cache-from/to: type=gha`. |
| `image-manifest` | `needs: [meta, image]`; `docker buildx imagetools create` per registry combining `:v<version>-amd64` + `:v<version>-arm64` into `:v<version>`, `:<version>`, `:amd64`/`:arm64` targets already exist, and `:latest` only when `prerelease == false`. |

> Amended after the first runner dispatch: the light tier passes `-short`, because the service-backed tests in `framework/vectorstore` and `plugins/semanticcache` gate on `-short`, not on `CI`, and so failed on refused ports. `full-tests.yml` still runs without it.

- [x] **Step 4: Lint**

Run: `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/release.yml`
Expected: no output, exit 0.

- [x] **Step 5: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: tag-triggered release of binaries and multi-arch images"
```

- [x] **Step 6: State the verification limit**

A release cannot be verified without pushing a tag. The first real verification is a deliberate `git tag v0.0.1-spike && git push origin v0.0.1-spike`, run by the user, expecting: prerelease flagged, 10 assets, four image tags per registry, no `latest`.

---

### Task 8: `full-tests.yml`

**Files:**
- Create: `.github/workflows/full-tests.yml`

**Interfaces:**
- Consumes: `setup-go-workspace.sh`, `tests/docker-compose.yml`, `make setup-mcp-tests`.
- Produces: `workflow_dispatch`-only workflow that runs the service-backed suites the light tier skips.

- [x] **Step 1: Write the workflow**

Steps: checkout; setup Go; setup Node; workspace; `docker compose -f tests/docker-compose.yml up -d`; wait loop `until curl -sf http://localhost:9000/v1/.well-known/ready; do sleep 2; done` (Weaviate) plus `nc -z localhost 50051` (gRPC) and `docker compose exec -T clickhouse wget --spider -q http://127.0.0.1:8123/ping`; `make setup-mcp-tests`; `make build-ui`; then `CI=1 go test -timeout 30m ./...` in `core`, `framework`, `transports`, `plugins/*/`; `docker compose -f tests/docker-compose.yml down -v` under `if: always()`.

- [x] **Step 2: Lint**

Run: `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/full-tests.yml`
Expected: no output, exit 0.

- [x] **Step 3: Commit**

```bash
git add .github/workflows/full-tests.yml
git commit -m "ci: on-demand full suite with service containers"
```

---

### Task 9: Remove the retired apparatus

**Files:**
- Delete: every `.github/workflows/*.yml` except `ci.yml`, `release.yml`, `full-tests.yml`
- Delete: `.github/workflows/requirements/`, `.github/workflows/tools/`, `.github/workflows/configs/`
- Delete: `.github/workflows/scripts/` — keep only `apt-sources-https.sh`, `install-cross-compilers.sh`, `setup-go-workspace.sh`, `harness-gateway.sh`, `test-integrations.sh`, `require-go-workspace.sh`, `run-core-unit-tests.sh`, `build-release-binary.sh`, `compute-image-tags.sh`, and the three `*.test.sh` files from this plan
- Delete: `npx/`, `terraform/`, `community/`, `tasks/`, `transports/Dockerfile`, `transports/Dockerfile.redhat`
- Modify: `.github/dependabot.yml`, `.github/actionlint.yaml`, `Makefile`

**Interfaces:**
- Produces: a tree where every remaining script is referenced by something that still exists.

- [x] **Step 1: Delete, then let the fallout name itself**

Run the deletions, then:
```bash
grep -rn "\.github/workflows/scripts/" --include=*.sh --include=*.yml --include=Makefile --include=*.mjs . | grep -v node_modules
grep -rn "npx/\|terraform/\|community/\|transports/Dockerfile" --include=*.sh --include=*.yml --include=Makefile --include=*.mdx --include=*.md . | grep -v node_modules | grep -v docs/changelogs
```
Expected: the second sweep reports the `docs/` mentions only (kept as history); the first reports only references to scripts on the keep-list after Step 2.

- [x] **Step 2: Repair remaining references**

- `Makefile`: `test-integrations` still points at `test-integrations.sh` (kept) — no change. Remove the `LOCAL` branch from `docker-image` if Task 2 left one. No target reads `*/version`; leave those files alone.
- `tests/e2e/api/runners/proxy/run-proxy-matrix.sh`: keeps using `harness-gateway.sh` (kept).
- `docs/contributing/setting-up-repo.mdx`: update the `make docker-image` line to drop `LOCAL=1`.
- `docs/plugins/building-dynamic-binary.mdx`: replace `-f transports/Dockerfile` / `-f transports/Dockerfile.debian` with `-f Dockerfile` / `-f Dockerfile.debian` and note `Dockerfile.debian` is not shipped.

- [x] **Step 3: Trim `.github/actionlint.yaml`**

Delete every entry under `self-hosted-runner` (no workflow uses `builder-8-32`, `arm-64-2cpu-8gb`, `codspeed-macro` anymore). If nothing remains, delete the file.

- [x] **Step 4: Trim `.github/dependabot.yml`**

Keep `gomod` entries for `/core`, `/framework`, `/transports`, `/cli`, `/plugins/*`; keep `github-actions` for `/`. Delete `docker`, every `npm` entry, and every `examples/` entry.

- [x] **Step 5: Verify nothing is dangling**

Run:
```bash
for f in $(grep -rho '\.github/workflows/scripts/[A-Za-z0-9._-]*\.sh' --include=Makefile --include=*.sh --include=*.yml . | grep -v node_modules | sort -u); do
  [ -f "$f" ] || echo "MISSING: $f"
done
ls .github/workflows/*.yml
```
Expected: no `MISSING:` lines; exactly `ci.yml`, `release.yml`, `full-tests.yml`.

- [x] **Step 6: Commit**

```bash
git add -A
git commit -m "chore: drop the bifrost release apparatus, npx packages and unused scripts"
```

---

### Task 10: Operator documentation

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: artifact names (Task 5), image names and tags (Tasks 4, 7), variable names (Global Constraints).

- [x] **Step 1: Write the section**

Append (or replace the upstream installation block with) exactly:

```markdown
## Build and run from source
    make build        # builds the UI and tmp/bifrost-http
    ./tmp/bifrost-http -app-dir ./data

## Release
1. git tag v2.6.0 && git push origin v2.6.0
2. Wait for the "Release" workflow to finish. It re-runs the unit tests before building.
3. Take `marga-runtime-<os>-<arch>` plus its `.sha256` from the GitHub Release, or pull
   docker pull <DOCKERHUB_USERNAME>/marga-runtime:v2.6.0
   docker pull ghcr.io/ilmannafi11/marga-runtime:v2.6.0
4. Run: ./marga-runtime-linux-amd64 -app-dir ./data
   or:  docker run -p 8080:8080 -v "$PWD/data:/app/data" <DOCKERHUB_USERNAME>/marga-runtime:v2.6.0

## Required repository configuration
| Kind | Name | Purpose |
|---|---|---|
| variable | DOCKERHUB_USERNAME | Docker Hub namespace |
| secret   | DOCKERHUB_TOKEN    | Docker Hub push token (read/write) |
`GITHUB_TOKEN` is used for GHCR and the GitHub Release; nothing else is needed.
A release fails if the Docker Hub values are missing.

## Versioning
The Git tag is the only version. The `*/version` files that came from upstream are
kept so merges stay clean, but nothing reads them.

## Docker image visibility
The GHCR package is private on first publish. Set it to public once in the package
settings if `docker pull ghcr.io/...` must work without a login.

## Syncing from upstream
    git fetch upstream && git merge upstream/dev

## Deliberate limit
CI (`ci.yml`) runs a light tier: no service containers, no MCP fixtures. Run
`make test-framework`, `make test-plugins`, or the "Full tests" workflow when a
change touches storage, queues, or MCP.
```

- [x] **Step 2: Verify the claims**

Run: `grep -n "marga-runtime" README.md | head` and confirm every artifact/tag/var name matches `compute-image-tags.sh` and `build-release-binary.sh` output.
Expected: names identical; no reference to `npx`, R2, `downloads.getmaxim.ai`, or `maximhq/bifrost` images.

- [x] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: release, install and upstream-sync steps for marga-runtime"
```
