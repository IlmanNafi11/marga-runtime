# Spec: Marga Runtime — Standalone Repository, CI and Release

- Date: 2026-11-17
- Status: open questions resolved; awaiting approval to implement (no implementation until approved)
- Owner: IlmanNafi11
- Repository: `IlmanNafi11/marga-runtime` (public fork of `maximhq/bifrost`)
- Source documents: `docs/superpowers/specs/2026-11-17-marga-runtime-release-design.md` (design), `docs/superpowers/plans/2026-11-17-marga-runtime-release.md` (implementation plan)
- This file is the contract the plan implements. Where the plan and this spec disagree, this spec wins until it is amended.

---

## 0. Capability map (Phase 0)

The request bundles capabilities that ship and verify independently. Module ids are stable; the plan's tasks are mapped to them.

| Module id | Responsibility | Depends on | Plan tasks |
|---|---|---|---|
| `m01-build-integrity` | `make build` and every release build compile the fork's own `core`/`framework`/`plugins` from `go.work`; `GOWORK=off` builds become impossible in CI | — | 1 |
| `m02-container-image` | One root `Dockerfile` built from the workspace; image boots, serves UI, reports version | `m01` | 2 |
| `m03-ci-light` | One `ci.yml` on PR + `dev`: build, vet, unit tests, i18n — no services, no secrets | `m01` | 3, 6 |
| `m04-release-pipeline` | Tag `v*` → 5 binaries + checksums + multi-arch images in GHCR and Docker Hub + GitHub Release | `m01`, `m03` | 4, 5, 7 |
| `m05-full-tests` | On-demand service-backed suite (postgres/weaviate/clickhouse/redis, MCP fixtures) | `m03` | 8 |
| `m06-repo-retirement` | Remove the upstream release apparatus; leave no dangling reference | `m02`, `m03`, `m04`, `m05` | 9 |
| `m07-operator-docs` | `README.md`: build, release, required config, versioning, upstream sync, deliberate limits | `m04`, `m05`, `m06` | 10 |

Build order: `m01 → m02 → m03 → m04 → m05 → m06 → m07`.

Dependency direction is one-way; no cycle. Cross-module interface is limited to three scripts and the two workflow entry points:

- `require-go-workspace.sh` — produced by `m01`, consumed by `Makefile` (`m01`), `ci.yml` (`m03`), `release.yml` (`m04`).
- `run-core-unit-tests.sh` — produced by `m03`, consumed by `ci.yml` (`m03`), `release.yml` `verify` job (`m04`), `full-tests.yml` shares the skip semantics (`m05`).
- `build-release-binary.sh`, `compute-image-tags.sh` — produced by `m04`, consumed only by `release.yml` (`m04`).

This spec is single-file at the user's request: the module sections in §9 are the per-module specs, indexed by the table above.

### Assumptions I am making (correct now, or they become defects)

1. The fork stays public; `ubuntu-24.04-arm` stays free and GHCR pulls are anonymous once visibility is flipped once.
2. Module paths stay `github.com/maximhq/bifrost/...`; upstream merges are wanted occasionally (design D1).
3. `dev` is the only long-lived branch; no `main` is created (design D8).
4. Docker Hub credentials already exist or will exist; a missing value fails the release loudly instead of skipping a registry.
5. Deleting `e2e-tests.yml`, `pr-tests.yml`, `ui-i18n.yml`, `live-tests.yml`, `docs-validation.yml`, `scorecards.yml`, `snyk.yml`, `dependency-review.yml`, `labeler.yml`, `labels.yml`, `codspeed.yml`, `openapi-*.yml`, `realtime-tests.yml`, `run-core-tests.yml`, `release-*.yml`, `npx-publish.yml`, `helm-release.yml`, `workflow-lint.yml`, `pr-test-notifier.yml`, `dependabot-alerts.yml` is intended — CI parity with upstream is a non-goal.
6. No benchmark/cost statement is required for this change: it touches build/release apparatus only and adds zero work to the request path (§9 "Cost" note).
7. Provider-harness cases are **not** required here. Exemption rationale in §7.4.

---

## 1. Objective

Manage `marga-runtime` as a standalone product repository, not as a mirror of a 40-engineer release org.

**Users**

- Operator (the owner): tags a version, gets a binary and an image, runs them.
- Future self / collaborator: runs `CI=1 make build` locally and gets a binary compiled from the tree in front of them.
- Anyone: `docker run` the published image.

**Success looks like**

- One tag produces one release: 5 platform binaries + 5 checksums, `marga-runtime` images on GHCR and Docker Hub for `amd64`/`arm64`, GitHub Release with notes.
- A PR to `dev` is gated by a build and unit tests in minutes, with no secret configured.
- No command in the repository can silently compile `github.com/maximhq/*` published modules instead of this tree.
- The release apparatus that exists is the only apparatus that exists — no orphan script, no orphan workflow, no reference to R2 / npx / Discord / `maximhq` images.

**Value on the request path:** none intended. This is build/release-only; runtime behavior, endpoints, config schema and `ui/` are out of scope (design Non-goals). Nothing here allocates or executes per request.

---

## 2. Scope

**In scope:** `.github/workflows/` (3 files), `.github/workflows/scripts/` (keep-list + 4 new scripts + 3 test scripts), `.github/dependabot.yml`, deletion of `.github/actionlint.yaml`, `.github/CODEOWNERS`, `.github/labels.yml`, `.github/labeler.yml`, `Makefile` (`build`, `build-cli`, `_build-with-docker`, `docker-image`), root `Dockerfile`, root `.dockerignore`, `README.md`, three docs build-command fixes, deletions of `npx/`, `terraform/`, `community/`, `tasks/`, `transports/Dockerfile`, `transports/Dockerfile.redhat`.

**Out of scope:** runtime code (`core/`, `framework/`, `plugins/`, `transports/*.go`), `ui/` source, `docs/` content beyond the three build-command fixes (V5, V6, V8c), Go module publishing, Helm, Nix, provider-harness, live-provider gating.

---

## 3. Verified baseline and required plan corrections

Verified in this worktree on 2026-11-17.

| # | Fact | Consequence |
|---|---|---|
| V1 | There is **no root `go.mod`**; `go.work` declares `go 1.27.4`, `core/go.mod` declares `go 1.27.3` | `m03`: `actions/setup-go` must use `go-version-file: go.work`. Plan Task 6 Step 1 as written (`go.mod`) fails. **Correction C1.** |
| V2 | `.dockerignore` excludes `.github/` | `m02`: the Dockerfile must be self-contained (`go work init` + `go work use`, as `transports/Dockerfile.local` already does). It must **not** call `.github/workflows/scripts/setup-go-workspace.sh`. |
| V3 | `.dockerignore` excludes `go.work`, `go.work.sum` | `m02`: both lines must go, exactly as plan Task 2 Step 2 says. Generic `*.md`/`docs/`/`tests/` exclusions stay. |
| V4 | `transports/Dockerfile.local` exists and indents every directive by two spaces | `m02`: promote it verbatim, de-indent, keep `ARG VERSION`, `-X main.Version=v${VERSION}`, `USER 1000:0`, `VOLUME`, `HEALTHCHECK /health`, `ENTRYPOINT /app/docker-entrypoint.sh`. |
| V5 | `docs/plugins/building-dynamic-binary.mdx:205,329` reference `transports/Dockerfile` and `transports/Dockerfile.debian`; after `m06` neither file exists (`Dockerfile.debian` does not exist today either) | `m06`: rewrite both "Build and run" blocks to `docker build -f -` with the inline Dockerfile from the Accordion above them; no doc may instruct building a file that is not in the tree. **Correction C2 (resolved Q1 = option b).** |
| V6 | `docs/contributing/setting-up-repo.mdx:219` documents `make docker-image (LOCAL=1 uses Dockerfile.local)` | `m06`: drop the `LOCAL=1` parenthetical; `docker-image` points at the single root file. |
| V7 | `.github/actionlint.yaml` lists only self-hosted labels (`arm-64-2cpu-8gb`, `builder-8-32`, `codspeed-macro`) | `m03`/`m04`: verify actionlint v1.7.12 accepts `ubuntu-24.04-arm`. If it does not, add that single label to `self-hosted-runner.labels`; do not downgrade the runner. **Correction C3.** |
| V8 | `npx/`, `terraform/`, `community/`, `tasks/` exist; `helm-charts/`, `nix/` exist and stay | `m06` deletion list is accurate; `helm-charts/`/`nix/` are retained but never released. |
| V8a | `.github/workflows/labels.yml` and `labeler.yml` both carry `if: github.repository_owner == 'maximhq'` and run on the custom runner `builder-8-32` | Both are dead in this fork: they never execute, and their runner label is the thing C3 removes. `m06`: delete them together with `.github/labels.yml`, `.github/labeler.yml`. **Resolved Q2.** |
| V8b | `.github/CODEOWNERS` assigns every path to `@maximhq/bifrost-admin` and still names removed paths (`terraform/`, `npx/*`, `scripts/bifrost-migration-cli/version`) | A public solo fork cannot satisfy a team slug: GitHub silently ignores it and the stale entries mislead. `m06`: delete `.github/CODEOWNERS`. **Resolved Q5.** |
| V8c | `docs/plugins/building-dynamic-binary.mdx:211-323` documents Option B (Debian/glibc) as a self-contained Accordion whose only repo-relative artifact is the `docker build -f transports/Dockerfile.debian` line | `m06`: keep the guide, replace the Alpine and Debian build commands with inline Dockerfiles piped to `docker build -f -`, and state that the `*Dockerfile.debian` variants are not shipped here. Deleting the section would remove the only explanation of glibc/dynamic-plugin tradeoffs. **Resolved Q1.** |
| V9 | `core/version=1.15.2`, `framework/version=1.12.2`, `transports/version=2.5.1`, `cli/version=1.0.0`; nothing in the new pipeline reads them | `m04`: tag is the only version source; files stay untouched so upstream merges stay clean. |
| V10 | `origin/HEAD -> origin/dev`; no `main` | `m03`: branch filter is `dev` only. No extra branch work. |
| V11 | `release-pipeline.yml:1034-1060` skips only `*/internal/llmtests` (both by path and by `grep -lq` on `*_test.go`) | `m03`: the extracted script must keep those two checks **and** add the same two for `mcptests`. |
| V12 | `Makefile` build recipe has three `$(if $(LOCAL),,GOWORK=off)` sites plus one in `build-cli` and two in `_build-with-docker` | `m01`: all six removed; `LOCAL` no longer means "workspace on" — the workspace is unconditional. |
| V13 | `Makefile` targets already reference `.github/workflows/scripts/setup-go-workspace.sh` (`1773`, `1778`) and `test-integrations.sh` (`1649`) | `m06` keep-list must include both, or those targets break. |

---

## 4. Commands

All commands run from the repository root unless stated. `CI` is the variable the guard reads.

```bash
# Workspace (required once per checkout; idempotent, no-op if go.work exists)
source .github/workflows/scripts/setup-go-workspace.sh

# Build (m01)
make build-ui                 # vite build + tsc + copy-build into transports/bifrost-http/ui
CI=1 make build               # static linux binary at tmp/bifrost-http, fails without go.work
make build-cli                # tmp/bifrost

# Script tests (m01, m03, m04) — each script ships a red/green test
bash .github/workflows/scripts/require-go-workspace.test.sh
bash .github/workflows/scripts/run-core-unit-tests.test.sh
bash .github/workflows/scripts/compute-image-tags.test.sh
bash .github/workflows/scripts/build-release-binary.test.sh

# Unit tests, light tier (m03)
bash .github/workflows/scripts/run-core-unit-tests.sh core
for m in framework transports plugins/*/; do ( cd "$m" && env -u CI go test -timeout 20m ./... ); done
(cd ui && npm run i18n:check)

# Vet (m03)
for m in core framework transports plugins/*/; do ( cd "$m" && go vet ./... ); done

# Local release binary (m04)
bash .github/workflows/scripts/build-release-binary.sh linux amd64 2.6.0 /tmp/marga-dist
/tmp/marga-dist/marga-runtime-linux-amd64 -port 18081 -app-dir /tmp/marga-data

# Image (m02)
docker build --build-arg VERSION=2.6.0 -t marga-runtime:smoke .
docker run -d --rm -p 18080:8080 -e APP_HOST=0.0.0.0 --name marga-smoke marga-runtime:smoke
curl -sf http://127.0.0.1:18080/health && curl -s http://127.0.0.1:18080/ | head -c 200

# Workflow lint (m03, m04, m05)
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yml
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/release.yml
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/full-tests.yml

# Release (operator only — never run by an agent)
git tag v2.6.0 && git push origin v2.6.0
git tag v0.0.1-spike && git push origin v0.0.1-spike   # prerelease rehearsal
```

---

## 5. Project structure

```text
.github/
├── workflows/
│   ├── ci.yml               # PR + push dev: build, vet, unit tests, i18n
│   ├── release.yml          # push tag v*: verify, 5 binaries, 2-arch images, release
│   ├── full-tests.yml       # workflow_dispatch: service-backed suite
│   └── scripts/
│       ├── apt-sources-https.sh          # used by install-cross-compilers.sh
│       ├── install-cross-compilers.sh    # darwin/windows toolchains
│       ├── setup-go-workspace.sh         # kept; used by Makefile, workflows
│       ├── require-go-workspace.sh       # new (m01) + .test.sh
│       ├── run-core-unit-tests.sh        # new (m03) + .test.sh
│       ├── compute-image-tags.sh         # new (m04) + .test.sh
│       ├── build-release-binary.sh       # new (m04) + .test.sh
│       ├── harness-gateway.sh            # kept; local make targets only
│       └── test-integrations.sh          # kept; `make test-integrations`
├── dependabot.yml           # gomod (core, framework, transports, cli, plugins/*) + github-actions (/)
└── actionlint.yaml          # deleted (m06): only standard runners remain
Dockerfile                   # root; build context = repo root (m02)
.dockerignore                # no go.work exclusions (m02)
Makefile                     # workspace unconditional; docker-image -> Dockerfile
README.md                    # build / release / config / versioning / sync / limits
```

Deleted (m06): every other `.github/workflows/*.yml`, `.github/workflows/{requirements,tools,configs}/`, the retired scripts listed in the plan, `npx/`, `terraform/`, `community/`, `tasks/`, `transports/Dockerfile`, `transports/Dockerfile.redhat`.

Untouched by design: `core/`, `framework/`, `plugins/`, `transports/` Go sources, `ui/`, `docs/` content (except V5/V6), `helm-charts/`, `nix/`, `tests/`, all `*/version`.

---

## 6. Code style

Shell scripts: `set -uo pipefail` minimum; `set -euo pipefail` for the guard; no `#!/bin/sh`; error lines lowercase to stderr and `exit 1`; wrong argument count `exit 2`; no color codes in CI scripts.

```bash
#!/usr/bin/env bash
# One line saying the invariant this script exists to protect.
set -euo pipefail

if [ ! -f go.work ] && [ -n "${CI:-}" ]; then
  echo "error: go.work is missing; builds must run against the local workspace" >&2
  exit 1
fi
exit 0
```

Workflows: `name:`, `on:`, `concurrency:`, explicit `permissions:`, `timeout-minutes:` on every job; pin actions by major tag (`actions/checkout@v6`); no `environment:`, no `step-security` egress hook, no secret in `ci.yml`.

Naming: artifact `marga-runtime-<goos>-<goarch>[.exe]`; image `marga-runtime`; GHCR ref `ghcr.io/ilmannafi11/marga-runtime`; Docker Hub `${DOCKERHUB_USERNAME}/marga-runtime`.

Go/Markdown conventions are inherited from `AGENTS.md` and unchanged (no underscore filenames, error strings lowercase, no trailing punctuation).

---

## 7. Testing strategy

Framework: plain bash test scripts beside the script under test (`<name>.test.sh`), plus `actionlint` for YAML and real build/boot smoke checks. No new Go test files — no Go source changes.

### 7.1 Red/green per script (mandatory, in this order)

1. Write `<name>.test.sh` first, run it, confirm it fails for the expected reason (missing script, wrong args), not a syntax error.
2. Implement the script.
3. Re-run: exit 0, `PASS` or silence.

Each test isolates `PATH` with a fake `go` where the real compiler is irrelevant, so it runs in ~10ms and never needs network.

### 7.2 Packaged verification per module

| Module | Checks |
|---|---|
| `m01` | guard test 3 cases; `CI=1 make build` green with `go.work`, and `exit=1` after `mv go.work /tmp/go.work.bak` (restore afterwards) |
| `m02` | `docker build` + run: `/health` 200, `/` returns HTML not 404/empty, log banner contains `v2.6.0` |
| `m03` | selector test (fake `go`, exact args `test -timeout 20m example/core/plain`); real run over `core`; `env -u CI` loop over `framework`/`transports`/`plugins/*`; i18n check; actionlint clean |
| `m04` | tag-computation test (stable moves `latest`, prerelease does not); binary test (CGO on, `sqlite_static` linux-only, `-buildid=`, unsupported boundary `exit 2`); one real linux/amd64 artifact booted and banner-checked; actionlint clean |
| `m05` | workflow lints; first live dispatch is operator-run — a workflow cannot be verified without running it |
| `m06` | reference sweep: `MISSING:` list empty; `ls .github/workflows/*.yml` prints exactly 3 files |
| `m07` | every name in README greps back to a script/constraint (artifact, tag, variable, image) |

### 7.3 Deliberate limit

Tier ringan means service-backed packages are not gated on PRs (`framework/logstore/clickhousestore_test.go:108` fails when ClickHouse is absent and `CI` is set). Compensated by `full-tests.yml` (manual) and local `make test-framework`. This limit is documented in README, not hidden.

### 7.4 Exemptions (explicit, per AGENTS.md)

- **Provider harness: exempt.** No bytes on the wire change: no handler, converter, provider or plugin source is touched. Release only changes how the binary is produced.
- **Benchmarks: exempt.** Nothing on the request path is added or moved; per-request cost is unchanged by construction.
- **E2E (Playwright): exempt.** No `ui/` change.

---

## 8. Boundaries

**Always**

- `source .github/workflows/scripts/setup-go-workspace.sh` (or rely on `Makefile`) before any `go build`/`go test` in CI; never export `GOWORK=off`.
- `CGO_ENABLED=1` for every build; `sqlite3` is a hard dependency.
- Keep module paths `github.com/maximhq/bifrost/...` and keep every `*/version` file byte-identical.
- Run the narrowest check for the module you touched, once, before committing: script test, then the build/smoke it gates.
- `actionlint` clean on each new workflow before commit.
- One commit per plan task, message prefix `build:`, `ci:`, `chore:`, `docs:`.

**Ask first**

- Dropping any platform from the matrix (`darwin/*`, `windows/amd64`) — design D5 change, user decision.
- Renaming artifacts, image name, registries, or tag scheme after `m04` lands.
- Adding any repository variable/secret beyond `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN`.
- Deleting anything under `docs/` beyond the two reference fixes (V5, V6).
- Re-enabling upstream module publishing, R2, npx, Helm release, or a live-provider gate.

**Never**

- Build with `GOWORK=off`, or resolve `github.com/maximhq/*` published modules in any release artifact.
- Introduce a second version source (version files, `transports/version`, `npx` package versions).
- Put a secret, token or Docker Hub password in a workflow file, `config.json`, or the README.
- Use QEMU emulation for the arm64 image (native `ubuntu-24.04-arm` only).
- Remove or weaken a test to make a gate pass; skip an unexpected HTTP status in any test assertion.
- Run the live provider harness, or push a release tag, on the user's behalf.
- Rewrite git history or force-push `dev`.

---

## 9. Module specs

### m01-build-integrity — depends on: —

**Objective.** Impossible to produce a green build that compiles code outside this repository.

**Deliverables.** `require-go-workspace.sh` + its test; `Makefile` `build`, `build-cli`, `_build-with-docker` free of `GOWORK=off`; guard invoked first in each recipe body.

**Acceptance.**
1. `require-go-workspace.test.sh` → exit 0, no output; fails on implementation absent.
2. Guard matrix: (`go.work` present, `CI=1`) pass; (`absent`, `CI` empty) pass; (`absent`, `CI=true`) fail with the documented message on stderr.
3. `CI=1 make build` produces `tmp/bifrost-http`; removing `go.work` makes the same command exit non-zero.
4. `grep -n 'GOWORK=off' Makefile` returns nothing.

### m02-container-image — depends on: m01

**Objective.** One image, built from the workspace, that actually serves the embedded UI.

**Deliverables.** Root `Dockerfile` (promoted from `transports/Dockerfile.local`, de-indented); `.dockerignore` without `go.work`/`go.work.sum`; `make docker-image` targets `Dockerfile` and tags `marga-runtime`, `marga-runtime:$(GIT_SHA)`, `marga-runtime:latest`.

**Acceptance.**
1. `docker build --build-arg VERSION=2.6.0 -t marga-runtime:smoke .` succeeds with context = repo root.
2. Container boots; `GET /health` OK; `GET /` returns HTML (not 404, not empty) — proves the embedded UI stage ran.
3. Startup banner contains `v2.6.0`.
4. `Dockerfile` matches the workspace-only copies (`core/`, `framework/`, `plugins/`, `transports/`) plus the self-contained `go work init` loop; it references no `.github/` path (excluded by `.dockerignore`).

### m03-ci-light — depends on: m01

**Objective.** PRs to `dev` gated in minutes with zero configured secrets.

**Deliverables.** `run-core-unit-tests.sh` + test; `.github/workflows/ci.yml`.

**Acceptance.**
1. Selector test green with exact args; script exits 1 with `error: no unit-test packages found` on an empty list; skips `*/internal/llmtests` and `*/internal/mcptests` by import path and by `*_test.go` import.
2. `ci.yml` runs on `pull_request` and `push: branches: [dev]`, `concurrency` cancel-in-progress, `permissions: contents: read`, `timeout-minutes` set.
3. `ci.yml` uses `go-version-file: go.work` (correction C1) and `node-version-file: .nvmrc`.
4. Steps: workspace → `make build-ui` → `make build` → artifact `bifrost-http-linux-amd64` → `go vet` per module → core unit tests via script → `env -u CI go test` per `framework`/`transports`/`plugins/*` → `npm run i18n:check`.
5. No `environment:`, no secret reference, no `step-security` egress step.
6. `actionlint` clean.

### m04-release-pipeline — depends on: m01, m03

**Objective.** One tag → one complete, reproducible release.

**Deliverables.** `compute-image-tags.sh`, `build-release-binary.sh` (+ tests); `.github/workflows/release.yml` with `meta`, `verify`, `binaries`, `publish-release`, `image`, `image-manifest`, and a `workflow_dispatch`-only `spike-cross` job.

**Acceptance.**
1. Tag computation: `2.6.0 amd64 <prefix>` emits `v2.6.0-amd64`, `2.6.0-amd64`, `v2.6.0`, `2.6.0`, `amd64`, `latest`; `2.7.0-rc.1` emits the same minus `latest`; wrong arg count → exit 2.
2. Binary script: linux/* use `sqlite_static` + `-extldflags '-static'` + `-buildid=`; darwin/windows omit both; `CGO_ENABLED=1` always; `.exe` for windows; sibling `.sha256` with a bare filename; unsupported boundary → exit 2; empty `transports/bifrost-http/ui` → exit 1 with the actionable message.
3. `release.yml` `meta` validates `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$` and sets `prerelease` from a `-` suffix; failure aborts before any build.
4. `verify` re-runs the m03 unit tier and gates `binaries`/`image`.
5. Release carries exactly 10 assets; `prerelease` flag follows the tag.
6. `image` fails fast when `vars.DOCKERHUB_USERNAME` is empty; pushes to both registries; `image-manifest` combines `-amd64` + `-arm64` into `v<version>`, `<version>`, and `latest` only for stable.
7. `actionlint` clean with no `.github/actionlint.yaml` present.
8. Verification limit stated: end-to-end proof requires the operator's `v0.0.1-spike` tag.

### m05-full-tests — depends on: m03

**Objective.** The service-backed coverage the light tier skips stays reachable on demand.

**Deliverables.** `.github/workflows/full-tests.yml` (`workflow_dispatch` only).

**Acceptance.**
1. Brings up `tests/docker-compose.yml` and waits for Weaviate HTTP 200, gRPC 50051, ClickHouse ping before testing.
2. Runs `make setup-mcp-tests`, `make build-ui`, then `CI=1 go test -timeout 30m ./...` in `core`, `framework`, `transports`, `plugins/*`.
3. `docker compose down -v` under `if: always()`.
4. `actionlint` clean.

### m06-repo-retirement — depends on: m02, m03, m04, m05

**Objective.** Nothing in the tree refers to something that no longer exists.

**Deliverables.** Deletions per plan Task 9 plus `.github/CODEOWNERS`, `.github/labels.yml`, `.github/labeler.yml` (V8a, V8b); trimmed `.github/dependabot.yml`; deleted `.github/actionlint.yaml`; docs build-command fixes (V5, V8c) and the `LOCAL=1` note removed (V6); `Makefile` `LOCAL` leftovers removed.

**Acceptance.**
1. `ls .github/workflows/*.yml` → exactly `ci.yml`, `full-tests.yml`, `release.yml`.
2. Every `.github/workflows/scripts/*.sh` named by `Makefile`, `*.sh`, `*.yml` exists — no `MISSING:` lines; the keep-list includes `setup-go-workspace.sh`, `test-integrations.sh`, `harness-gateway.sh`, `apt-sources-https.sh`, `install-cross-compilers.sh` and the four new scripts with their tests (V13).
3. `.github/workflows/{requirements,tools,configs}/`, `npx/`, `terraform/`, `community/`, `tasks/`, `transports/Dockerfile`, `transports/Dockerfile.redhat`, `.github/CODEOWNERS`, `.github/labels.yml`, `.github/labeler.yml` are gone.
4. `grep -rn 'Dockerfile\.\(debian\|redhat\)\|-f transports/Dockerfile' docs README.md` returns nothing; no doc instructs building a file that does not exist.
5. `dependabot.yml` has `gomod` for `/core`, `/framework`, `/transports`, `/cli`, `/plugins/*` and `github-actions` for `/` only.
6. `grep -rn 'GOWORK=off\|maximhq/bifrost.*image\|downloads.getmaxim.ai\|upload-to-r2' Makefile .github Dockerfile README.md` returns nothing.
7. `grep -rn 'maximhq/' .github` returns nothing (no dead team slug, no dead repo gate).

### m07-operator-docs — depends on: m04, m05, m06

**Objective.** An operator who has never read this spec can build, release, pull and run.

**Deliverables.** `README.md` sections: build from source, release steps, required repository configuration, versioning (tag-only), Docker image visibility, syncing from upstream, deliberate CI limit.

**Acceptance.**
1. Every artifact name, tag, variable and secret in README matches the scripts' output (`compute-image-tags.sh`, `build-release-binary.sh`).
2. No reference to `npx`, R2, `downloads.getmax.ai`, or `maximhq/bifrost` images.
3. GHCR private-on-first-publish is stated as a one-time manual step.
4. "Deliberate limit" names `full-tests.yml` and the local `make test-*` equivalents.

---

## 10. Success criteria (global, testable)

| # | Criterion | Check |
|---|---|---|
| S1 | No build path compiles outside this tree | guard test + `grep 'GOWORK=off'` empty + image/release scripts source nothing external |
| S2 | Binary serves UI and reports version | `/` HTML + `/health` 200 + banner version on binary and image |
| S3 | Tag defines the version, exclusively | `meta` job + no `version` file read anywhere in `.github/` |
| S4 | Prerelease never moves `latest` | `compute-image-tags.test.sh` |
| S5 | 5 binaries + 5 checksums per release, 2 arch × 2 registries | rehearsal tag; artifact/tag inventory |
| S6 | PR gate needs no secrets and finishes fast | `ci.yml` inspection + first run wall-clock recorded |
| S7 | Retired apparatus leaves no dangling reference | deletion sweep outputs |
| S8 | README is sufficient alone | name-grep cross-check |

## 11. Risks

| Risk | Mitigation |
|---|---|
| darwin/windows cross-toolchain absent on runner | `spike-cross` (`workflow_dispatch`) before full implementation; dropping platforms requires user approval |
| `ubuntu-24.04-arm` handling in actionlint | Correction C3 revised: standard image, so delete `actionlint.yaml` instead of allowlisting a label; verified in `m03` before `m04` |
| ClickHouse/Weaviate test `t.Fatalf` under `CI` in the light tier | `env -u CI` loop; documented as deliberate limit |
| Docker Hub variables unset on first release | fail fast with an explicit message; README setup step |
| `go.work` newer than the pinned Docker Go image (1.27.4 vs 1.27.3) | `GOTOOLCHAIN=auto` downloads the toolchain inside the build stage; verify in `m02` smoke |
| Upstream changes `transports/Dockerfile` | that file no longer exists here; conflict is localized to one removed path |

## 12. Open questions — resolved 2026-11-17

| # | Question | Decision | Rationale |
|---|---|---|---|
| Q1 | Debian snippet in `building-dynamic-binary.mdx`: delete or keep with a note? | **Keep the section, rewrite the command** (option b, strengthened) | The Accordion is self-contained (inline Dockerfile), so nothing references a missing file once `docker build -f transports/Dockerfile.debian` becomes `docker build -f -`. Deleting it would drop the only explanation of glibc vs musl for dynamic plugins. |
| Q2 | Delete `.github/workflows/labels.yml` / `labeler.yml`? | **Delete**, with `.github/labels.yml` and `.github/labeler.yml` | Both jobs are gated `if: github.repository_owner == 'maximhq'` and target `builder-8-32`; they can never fire in this fork, and the runner label conflicts with C3. |
| Q3 | GHCR visibility flip manual, once-only? | **Yes, manual** | The API needs the package to exist first; the workflow publishes with `GITHUB_TOKEN` and cannot reliably change package visibility on first push. README states the one-time step; `m04` does not attempt it. |
| Q4 | Light tier acceptable (broken ClickHouse query not caught on PR)? | **Yes — accepted and documented** | Service containers on every PR cost minutes and flakiness for a solo repo; `full-tests.yml` + local `make test-framework` gate the tag. Recorded as a deliberate limit in README (`m07`). |
| Q5 | (new) `.github/CODEOWNERS` still points at `@maximhq/bifrost-admin` and removed paths | **Delete** | Unsatisfiable on a public solo fork; stale entries for `terraform/`, `npx/*`, `scripts/bifrost-migration-cli/version` actively mislead. |

## 13. Approval gate

Implementation does not start until this spec is approved. All open questions are resolved (§12); no implementation decision is outstanding. On approval, the existing plan (`docs/superpowers/plans/2026-11-17-marga-runtime-release.md`) executes in module order `m01 → m07`, with corrections C1–C3 and V5/V6/V8a–V8c folded into tasks 6, 9 and 10 before their steps run.
