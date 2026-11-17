# marga-runtime

A standalone build of the Bifrost AI gateway: one OpenAI-compatible endpoint in front of 20+ LLM providers, with MCP tool calling, semantic caching, governance and a built-in web UI.

This repository is a fork. It ships **its own** `core`, `framework`, `plugins` and `ui` from the workspace in this tree — never the published `github.com/maximhq/*` modules. Runtime behavior, configuration schema and endpoints are unchanged; the docs at [docs.getbifrost.ai](https://docs.getbifrost.ai) apply as-is.

## What is in the tree

```text
core/          # gateway engine, provider implementations
framework/     # config store, log store, vector store, streaming
transports/    # bifrost-http server, SDK compatibility layers
plugins/       # governance, telemetry, logging, semantic cache, otel, ...
ui/            # React dashboard embedded into the binary
docs/          # Mintlify documentation site
tests/         # provider harness, integration and E2E suites
Dockerfile     # the one image definition, built from this workspace
.github/workflows/
  ci.yml         # PR + dev: build, vet, unit tests, i18n
  release.yml    # v* tag: binaries, GitHub release, multi-arch images
  full-tests.yml # manual: service-backed suite
```

## Build from source

Requires Go 1.27.3+ (the workspace declares 1.27.4 via `GOTOOLCHAIN=auto`), Node from `.nvmrc`, a C compiler and `sqlite3` — CGO is mandatory.

```bash
# 1. Create go.work if this checkout has none (no-op when it exists)
bash .github/workflows/scripts/setup-go-workspace.sh

# 2. Build the UI, then the binary -> tmp/bifrost-http
make build-ui
make build

# 3. Run it
./tmp/bifrost-http -app-dir ./tmp/bifrost-data
```

`make build` refuses to run when `go.work` is missing and `CI` is set: a workspace-less build would silently compile published `github.com/maximhq/*` modules instead of this tree.

### Container image

```bash
make docker-image                       # marga-runtime, :<git-sha>, :latest
docker run -p 8080:8080 -e APP_HOST=0.0.0.0 marga-runtime
```

The image is defined once, in the root `Dockerfile`, and is self-contained: it copies `core/ framework/ plugins/ transports/` and runs `go work init` itself, so it needs no script from `.github/`.

## Releasing

Versioning is **tag-only**. Nothing reads `core/version`, `framework/version`, `transports/version` or `cli/version`; they stay byte-identical to upstream so merges stay clean.

```bash
git tag v2.6.0
git push origin v2.6.0
```

`release.yml` then:

1. **validates the tag** (`v<major>.<minor>.<patch>[-prerelease]`) and derives `prerelease` — a malformed tag aborts before anything is built;
2. **re-runs the unit tier** and gates publishing on it;
3. **builds five binaries** with their checksums — `linux/amd64`, `linux/arm64` (native ARM runner, no QEMU), `darwin/amd64`, `darwin/arm64`, `windows/amd64` — through `build-release-binary.sh`;
4. **publishes the GitHub release** with the ten assets (`generate_release_notes`, prerelease flag from the tag);
5. **builds and pushes one image per architecture** to GHCR and Docker Hub through `compute-image-tags.sh`, then **combines** them into a multi-arch manifest.

Image tags per release:

| Tag | Stable `v2.6.0` | Prerelease `v2.7.0-rc.1` |
|---|---|---|
| `v<version>-<arch>`, `<version>-<arch>` | pushed per arch | pushed per arch |
| `v<version>`, `<version>` | manifest | manifest |
| `<arch>` | pushed per arch | pushed per arch |
| `latest` | manifest | **not moved** |

Registries: `ghcr.io/ilmannafi11/marga-runtime` and `<DOCKERHUB_USERNAME>/marga-runtime`.

## Required repository configuration

| Name | Kind | Used by | Without it |
|---|---|---|---|
| `DOCKERHUB_USERNAME` | repository **variable** | `image`, `image-manifest` | the job fails fast — binaries and the GitHub release still publish |
| `DOCKERHUB_TOKEN` | repository **secret** (Docker Hub access token) | `image`, `image-manifest` | Docker Hub login fails |
| `GITHUB_TOKEN` | automatic | checkout, GHCR push, release | — |

Nothing else is configured; `ci.yml` needs no secret at all.

After the **first** publish, flip the GHCR package to public once, in the package settings. The workflow pushes with `GITHUB_TOKEN` and cannot change package visibility on first push.

## Tests

| Tier | Command | Runs on |
|---|---|---|
| Light | `bash .github/workflows/scripts/run-core-unit-tests.sh core` then `env -u CI go test ./...` per module | every PR and push to `dev` (`ci.yml`) |
| Full | `make test-framework`, `make test-plugins`, `make test-mcp`; or dispatch `full-tests.yml` | on demand (`full-tests.yml`) |
| Scripts | `bash .github/workflows/scripts/*.test.sh` | locally, milliseconds each |

**Deliberate limit:** the light tier runs with `CI` unset and no service containers, so packages that need ClickHouse, Weaviate, Redis or Postgres are not gated on a pull request. `full-tests.yml` (manual dispatch) and the local `make test-*` targets above are the gate before a tag. This is a recorded trade-off for a single-operator repository, not an oversight.

## Syncing from upstream

Upstream is `maximhq/bifrost`; the remote that tracks it in this clone may be named anything, so check `git remote -v` before the fetch.

```bash
git fetch upstream          # or whatever local name points at maximhq/bifrost
git merge upstream/dev
```

Conflicts concentrate in what this fork deleted (the upstream release workflows, the npx and Terraform trees) and in the `Makefile` build targets; keep the workspace-only build contract when resolving.

## License

Apache 2.0 — see [LICENSE](LICENSE).
