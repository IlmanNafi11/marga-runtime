# Marga Runtime — rilis & deploy untuk solo developer

Tanggal: 2026-11-17
Status: menunggu persetujuan

## Intent

`IlmanNafi11/marga-runtime` adalah fork `maximhq/bifrost` yang akan dikelola sendiri. Tujuan utamanya bukan menambah fitur, melainkan **menghapus kompleksitas rilis yang hanya masuk akal untuk tim**: pipeline 22 job, gerbang approval ponsel, publikasi 16 modul Go ke namespace yang bukan milik kita, R2, Discord, dan seterusnya.

Yang harus tetap benar setelah perubahan: rilis menghasilkan **binary tunggal** dan **image Docker** yang bisa dipakai untuk deploy, dan keduanya bisa dibuat ulang kapan saja dari repo.

## Keputusan yang sudah diambil

| # | Keputusan | Alasan |
|---|---|---|
| D1 | Path modul Go tetap `github.com/maximhq/bifrost/...` | Sync `upstream/dev` sesekali tetap mungkin; rename = 2021 file dan semua merge upstream jadi konflik |
| D2 | Build dari source tree lokal (workspace Go), bukan modul terpublikasi | Namespace `maximhq` tidak bisa kita publikasikan; `Dockerfile.local` sudah memakai `go work use` untuk semua modul |
| D3 | Satu nomor versi per rilis, dari tag Git `vX.Y.Z` | Tidak perlu 16 file `version`, tidak perlu memutuskan tiap modul naik/tidak |
| D4 | Registry: GHCR (`ghcr.io/ilmannafi11/marga-runtime`) **dan** Docker Hub (`<DOCKERHUB_USERNAME>/marga-runtime`) | Diminta; kredensial Docker Hub lewat repo variables/secrets |
| D5 | Platform binary: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64` | Diminta. `windows/arm64` tidak ditambahkan |
| D6 | Artefak binary diunggah ke GitHub Release, bukan R2 | `GITHUB_TOKEN` cukup, tidak ada akun/rahasia tambahan |
| D7 | `ci.yml` memakai tier **ringan** (~6–8 menit): tanpa service Docker, tanpa MCP fixture; `full-tests.yml` manual memegang cakupan layanan | PR tetap cepat; cakupan penuh tetap bisa diminta saat perlu |
| D8 | Branch utama tetap **`dev`**; tidak ada `main` | Konsisten dengan upstream, sync `merge upstream/dev` langsung, satu langkah lebih sedikit yang bisa terlupa |
| D9 | Repo **publik** | Runner `ubuntu-24.04-arm` gratis dan paket GHCR bisa ditarik anonim |
| D10 | `release.yml` **menjalankan ulang unit test ringan** sebelum membangun | Tag pada commit yang jelas rusak tidak lagi menghasilkan rilis |

## Goals

- Satu tag → satu rilis: binary (5 platform) + image Docker (2 arch, 2 registry) + GitHub Release dengan checksum.
- Alur PR di `dev`: build + unit test ringan, tanpa rahasia apa pun, selesai dalam hitungan menit.
- Setiap perintah yang dibutuhkan operator terdokumentasi di `README.md` (satu bagian "Release").
- `make build` dan `make docker-image` tetap bisa dipakai lokal tanpa CI.

## Non-goals

- Tidak merilis modul Go ke proxy publik (`core`, `framework`, `plugins/*`).
- Tidak menerbitkan paket `npx/`, Helm chart, atau docs Mintlify.
- Tidak mempertahankan provider harness live (~90 menit, butuh ~40 API key) sebagai gate rilis.
- Tidak mengubah arsitektur runtime, endpoint, atau skema config.
- Tidak menyentuh `ui/`, `core/`, `framework/`, `plugins/` kecuali perubahan `Makefile` dan `transports/Dockerfile*`.

## Anti-goals

- Rilis yang butuh approval manual di tengah jalan.
- Dua sumber kebenaran versi (file `version` **dan** tag).
- Pipeline yang bergantung pada rahasia milik organisasi lain.
- Merge upstream yang jadi mustahil karena perubahan struktural besar (rename modul, konsolidasi `go.mod`).

## Constraints

- **CGO wajib.** `framework/go.mod:23` memakai `github.com/mattn/go-sqlite3`; `CGO_ENABLED=0` tidak bisa.
- **Linux memakai tag `sqlite_static`** + `-extldflags '-static'` (pola `.github/workflows/scripts/build-executables.sh:92-95`).
- **Build selalu dari workspace.** `GOWORK=off` menarik versi `maximhq` yang sudah dipublikasikan, yaitu kode yang salah. `make build` diubah agar memakai workspace secara default dan gagal bila `go.work` tidak ada saat `CI` diset.
- **UI ter-embed.** `transports/bifrost-http/main.go:74` `//go:embed all:ui`; direktori `transports/bifrost-http/ui` harus ada sebelum compile. `make build-ui` menanganinya (`ui/package.json:build` → `vite build` + `tsc` + `copy-build`).
- **Repo publik.** Runner arm64 gratis; paket GHCR bisa ditarik anonim setelah visibilitasnya diubah sekali di UI GitHub.
- **Tier ringan tidak memuat service.** `framework/logstore/clickhousestore_test.go:108` dan `framework/queue/clickhousestore_test.go` memanggil `t.Fatalf` bila ClickHouse tidak ada selama `CI` diset; `core/internal/mcptests` butuh fixture `make setup-mcp-tests`. Tier ringan mengosongkan `CI` untuk paket berlayanan alih-alih menambah Docker.

## Approaches yang dipertimbangkan

**A. Tag-triggered release dari source tree lokal** — dipilih.
`v*` tag → uji ulang unit → build UI → build 5 binary → build+push image 2 arch ke 2 registry → GitHub Release.

**B. Rilis otomatis dari push ke `dev` dengan `transports/version`** — ditolak.
Lebih dekat ke upstream, tapi mempertahankan dua sumber versi dan membuat setiap push ke `dev` berpotensi merilis tidak sengaja.

**C. Konsolidasi semua modul menjadi satu `go.mod`** — ditolak.
Memutus D1; setiap merge upstream berubah menjadi konflik struktural.

## Desain

### 1. Workflow yang tersisa

```
.github/workflows/
├── ci.yml          # pull_request + push ke dev
├── release.yml     # push tag v*
└── full-tests.yml  # workflow_dispatch: cakupan layanan penuh
```

Semua workflow lain dihapus. `.github/dependabot.yml` dipertahankan hanya untuk `gomod` + `github-actions` pada direktori yang benar-benar dibangun (root workspace modules, `cli`, `transports`); entri `docker`, `npm`, dan `examples/` dihapus.

**`ci.yml`** — job `build`:
1. checkout, setup Go, setup Node (`.nvmrc`), `source .github/workflows/scripts/setup-go-workspace.sh`.
2. `make build-ui`.
3. `make build` — gagal bila `go.work` tidak ada (gerbang anti-`GOWORK=off`), statis, amd64 native.
4. `go vet ./...` di `core`, `framework`, `transports`, dan `plugins/*`.
5. Unit test tier ringan:
   - `core`: semua paket kecuali `internal/llmtests`, `internal/mcptests`, dan paket yang mengimpornya — `scripts/run-core-unit-tests.sh`.
   - `framework`, `transports`, `plugins/*`: `CI="" go test ./...` sehingga tes yang butuh layanan melewati dirinya sendiri alih-alih gagal.
6. `cd ui && npm run i18n:check`.

Tidak ada rahasia, tidak ada `environment`, tidak ada hook egress `step-security`.

**`full-tests.yml`** — `workflow_dispatch` saja: `docker compose -f tests/docker-compose.yml up -d`, tunggu ClickHouse/Weaviate/Redis siap, `make setup-mcp-tests`, lalu `go test ./...` di `core`, `framework`, `transports`, dan semua plugin. Pengganti manual atas `test-framework.sh`/`test-all-plugins.sh` yang dihapus.

**`release.yml`** — dipicu `push: tags: ["v*"]`:

| Job | Isi |
|---|---|
| `meta` | Turunkan `VERSION=${GITHUB_REF_NAME}`; gagalkan bila tidak cocok `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`; set `prerelease=true` bila ada sufiks |
| `verify` | Menjalankan unit test tier ringan yang sama dengan `ci.yml` lewat script yang sama. Rilis berhenti di sini bila merah |
| `binaries` | Matrix 5 sel, `needs: verify`. `linux/amd64` native (tag statis). `linux/arm64` di `ubuntu-24.04-arm` native. `darwin/amd64`, `darwin/arm64`, `windows/amd64` lewat `install-cross-compilers.sh` yang sudah ada. Satu script `build-release-binary.sh <goos> <goarch> <version>`; keluaran `marga-runtime-<os>-<arch>[.exe]` + `.sha256` |
| `publish` | `softprops/action-gh-release`, `prerelease` mengikuti `meta`, `generate_release_notes: true`, semua artefak `binaries` |

### 2. Image Docker

- `transports/Dockerfile.local` dipromosikan menjadi `Dockerfile` di root repo (konteks build = root). `transports/Dockerfile` (berbasis modul terpublikasi) dan `Dockerfile.redhat` dihapus.
- Nama image `marga-runtime`; tag per rilis `vX.Y.Z` dan `X.Y.Z`, plus `-amd64`/`-arm64` yang tetap tersedia sebagai tag konsumen; `latest` hanya dipindah oleh rilis stabil.
- Build: `docker/setup-buildx-action` + `docker/build-push-action` per arch (`linux/amd64` di `ubuntu-latest`, `linux/arm64` di `ubuntu-24.04-arm`, tanpa QEMU), lalu `docker buildx imagetools create` untuk manifest multi-arch.
- Registry & login:
  - GHCR: `GITHUB_TOKEN`, `packages: write`; `ghcr.io/${github.repository_owner,,}/marga-runtime` (nama wajib lowercase).
  - Docker Hub: `${{ vars.DOCKERHUB_USERNAME }}` + `${{ secrets.DOCKERHUB_TOKEN }}`; `${vars.DOCKERHUB_USERNAME}/marga-runtime`.
  - Keduanya dipush di job yang sama; bila `vars.DOCKERHUB_USERNAME` kosong, rilis gagal dengan pesan jelas.

### 3. Script yang dipertahankan

| Script | Dipakai oleh |
|---|---|
| `.github/workflows/scripts/setup-go-workspace.sh` (`Makefile`, `ci.yml`, `release.yml`, `Dockerfile`) | |
| `.github/workflows/scripts/run-core-unit-tests.sh` (baru, adaptasi daftar paket `release-pipeline.yml:1030-1062` + pengecualian `mcptests`) | `ci.yml`, `release.yml` |
| `.github/workflows/scripts/build-release-binary.sh` (baru, adaptasi `build-executables.sh`) | `release.yml` |
| `.github/workflows/scripts/install-cross-compilers.sh` | `release.yml` (darwin/windows) |
| `.github/workflows/scripts/harness-gateway.sh` | `Makefile` target test lokal, tidak dipakai CI |
| `.github/workflows/scripts/apt-sources-https.sh` | dipakai script test yang masih ada |

Dihapus: `release-*.sh`, `detect-all-changes.sh`, `test-*.sh` yang terikat pipeline lama, `upload-to-r2.sh`, `configure-r2.sh`, `resolve-*.sh`, `create-npx-release.sh`, `.github/workflows/requirements/`, `.github/workflows/tools/`, `.github/workflows/configs/`.

### 4. Versi & identitas

- Tag Git satu-satunya sumber versi. File `*/version` tetap ada supaya merge upstream tidak konflik, tetapi `README.md` menyatakan tidak dipakai rilis.
- `Makefile` `VERSION ?= dev-build` tidak berubah; `release.yml` memanggil `build-release-binary.sh` dengan `VERSION=${GITHUB_REF_NAME#v}`.
- Dihapus: `npx/`, `terraform/`, `community/`, `tasks/`. `docs/` dipertahankan apa adanya; `helm-charts/` dan `nix/` tetap ada tetapi tidak dirilis.

### 5. Dokumentasi operator (`README.md`)

```
## Release
1. git tag v2.6.0 && git push origin v2.6.0
2. Tunggu workflow "Release" hijau (menjalankan ulang unit test sebelum build).
3. Ambil binary + .sha256 di GitHub Release; atau
   docker pull <DOCKERHUB_USERNAME>/marga-runtime:v2.6.0
   docker pull ghcr.io/ilmannafi11/marga-runtime:v2.6.0   # anonim, publik
4. Jalankan: ./marga-runtime -app-dir ./data   (atau mount /app/data di Docker)

## Secrets & variables yang dibutuhkan
- vars.DOCKERHUB_USERNAME
- secrets.DOCKERHUB_TOKEN (scope: read/write)
GITHUB_TOKEN otomatis. Rilis gagal bila keduanya kosong.

## Sync dari upstream
git fetch upstream && git merge upstream/dev
```

## Yang diserahkan ke builder

- Daftar paket unit test di `run-core-unit-tests.sh` harus identik perilakunya dengan ekspresi lama, ditambah pengecualian `internal/mcptests`.
- `install-cross-compilers.sh` dipakai apa adanya untuk darwin/windows; bila paket tidak tersedia di runner, variasi toolchain diputuskan di spike.
- Format nama artefak final: `marga-runtime-<goos>-<goarch>[.exe]`.

## Risiko & mitigasi

| Risiko | Mitigasi |
|---|---|
| Build darwin/windows butuh cross-compiler (`o64-clang`, `x86_64-w64-mingw32-gcc`) yang bisa saja tidak ada di runner | Spike `workflow_dispatch` sebelum implementasi penuh: satu job mengompilasi darwin/amd64+arm64 dan windows/amd64. Turunkan platform **hanya dengan persetujuan user** |
| Tier ringan membuat paket berlayanan tidak digerbang di PR | Ditutup `full-tests.yml` manual dan `make test-framework` lokal; dicatat di README sebagai batas yang disengaja |
| Image `Dockerfile` baru lebih besar dari image upstream | Sama seperti `Dockerfile.local` hari ini; tidak ada regresi vs build lokal |
| Merge upstream menyentuh `Dockerfile` | `transports/Dockerfile` hilang dari tree kita; bila upstream mengubahnya, konfliknya terlokalisasi di satu file |
| `vars.DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` belum disetel saat rilis pertama | Rilis gagal di job image dengan pesan eksplisit; langkah penyiapan ada di README |

## Catatan evaluasi

- `transports/Dockerfile:41` = `GOWORK=off` → image lama menarik modul terpublikasi, bukan kode fork. Bug laten untuk fork mana pun, dan alasan kuat D2.
- `.github/workflows/scripts/release-bifrost-http-prep.sh:98-105` menulis ulang `go.mod` lalu commit saat rilis. Tidak dipakai lagi, jadi riwayat fork tidak lagi berisi commit versi otomatis.
- `e2e-tests.yml` menembak branch fitur yang tidak ada di `dev` — bukti CI lama memang sudah tidak utuh.
- Branch lokal dan `origin/HEAD` sama-sama `dev`; tidak ada `main`, jadi D8 tidak memerlukan pekerjaan tambahan selain menulis trigger `dev`.
