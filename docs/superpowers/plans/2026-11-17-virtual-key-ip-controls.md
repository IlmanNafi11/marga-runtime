# Virtual Key Client-IP Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two virtual-key-level client-IP controls — a unique-IP limit over a reset window, and an explicit allowed-IP list — enforced by the governance plugin on every governed pipeline.

**Architecture:** IP fields ride the existing `TableRateLimit` row (token/request pattern); the allowed-IP list is a JSON column on `TableVirtualKey`. Enforcement lives in the governance `Evaluate` funnel (`evaluateAccess` for the list, `evaluateLimits` for the limit); the unique-IP set is in-memory store state recorded at admission. The transport resolves the client IP when any loaded VK carries IP controls, independent of `logs_store.capture_client_ip`.

**Tech Stack:** Go (fasthttp transport, GORM configstore, sync.Map-based governance store), React + TanStack + react-hook-form UI, Playwright E2E.

**Spec:** `docs/superpowers/specs/2026-11-17-virtual-key-ip-controls-design.md` — read it first; it holds the decisions this plan argues from.

## Global Constraints

- Virtual-key scoped only: team, customer, and provider-config rate-limit bodies must **reject** `ip_max_limit` with a validation error, never silently drop it.
- Zero cost for keys without IP config: nil checks only; no header reads when no loaded VK carries IP controls.
- Enforcement never persists IPs to logs; the logging plugin keeps following `capture_client_ip` alone.
- Fail closed on the allowed list when no usable IP can be resolved; fail open (skip) on the unique-IP limit.
- IP normalization before any comparison: trim, strip port/zone, `net.ParseIP`; unparseable = unresolvable.
- New `Decision` values: none. List violation → `DecisionAccessBlocked`; IP-limit violation → `DecisionRateLimited`.
- No underscore filenames in Go; JSON tags `snake_case`; error strings lowercase, no trailing punctuation.
- `transports/config.schema.json` is the source of truth: update it before handlers.
- Duration grammar and validation mirror token/request exactly (`30s`…`1Y`, positive, required when a max is set).
- Every new user-visible UI string goes through i18n (`ui/locales/en` + all 10 other locales, real translations); `npm run i18n:check` must exit 0.
- Format only changed files (`cd ui && npm run format:changed`); Go: gofmt/goimports.

## Review Focus

1. **Allowed list configured but IP unresolvable/unparseable** → must 403, never serve. Test in Task 4 (unit) and Task 6 (harness).
2. **`ShouldEnforceClientIP` recompute missed on a mutation path** → with `capture_client_ip` off, a newly configured key 403s everything until restart. Tests in Task 5: one per recompute point (VK create/update/delete, rate limit upsert/delete, config.json sync, plugin reload).
3. **Spoof-shaped matching** — raw string compare of `ClientForwardedIP` output would let `2001:0db8::1` bypass a `2001:db8::1` CIDR and `1.2.3.4:5678` dodge an exact-IP list. Test in Task 3: normalization cases (v4-mapped IPv6, port suffix, zone, whitespace, garbage).
4. **Concurrent first requests from different IPs** must not both pass a full set — check-and-insert is one atomic step. Test in Task 3 (parallel goroutines at the boundary).
5. **Non-VK holder leak** — a team/customer/provider body carrying `ip_max_limit` must be refused, and a non-VK-held limit must never evaluate an IP window. Test in Task 2 (handler gate) and Task 3 (holder filter).

---

### Task 1: Schema, tables, validation, config hashes

**Files:**
- Modify: `framework/configstore/tables/ratelimit.go` (TableRateLimit fields + BeforeSave)
- Modify: `framework/configstore/tables/virtualkey.go` (AllowedIPs field + validation + BeforeSave call)
- Modify: `framework/configstore/clientconfig.go` (GenerateRateLimitHash, GenerateVirtualKeyHash)
- Modify: `framework/configstore/migrations.go` (add columns via `addColumnIfNotExists`)
- Test: existing `framework/configstore/tables/ratelimit_test.go`-style coverage — add to the existing table test files, never new ones

**Interfaces:**
- Consumes: existing `BeforeSave` validation patterns, `ParseDuration`, `addColumnIfNotExists`.
- Produces:
  - `TableRateLimit.IPMaxLimit *int64` (json `ip_max_limit,omitempty`), `TableRateLimit.IPResetDuration *string` (json `ip_reset_duration,omitempty`)
  - `TableVirtualKey.AllowedIPs []string` (gorm `type:text;serializer:json`, json `allowed_ips,omitempty`)
  - `func ValidateAllowedIPs(entries []string) error` in the tables package — accepts single IPv4/IPv6 and CIDR (`net.ParseIP` / `net.ParseCIDR`), rejects everything else including empty strings
  - Hash functions incorporate the new fields (rate limit: unconditionally; VK: only when `AllowedIPs != nil`, mirroring the `ListedModels` presence rule so pre-existing rows keep their hash)

- [ ] **Step 1: Write failing tests** — in the existing table test files: `BeforeSave` rejects `IPMaxLimit` without `IPResetDuration`, rejects zero/negative max, rejects unparsable duration; accepts a valid pair; `ValidateAllowedIPs` accepts `10.0.0.1`, `2001:db8::1`, `10.0.0.0/24`, `2001:db8::/32` and rejects `10.0.0.0/33`, `999.1.1.1`, `""`, `10.0.0.1/32 ` with a trailing space untrimmed? (no — trailing whitespace is rejected; normalization happens at enforcement, not storage); hash changes when `IPMaxLimit`/`AllowedIPs` change and does not when both nil.
- [ ] **Step 2: Run to verify fail** — `go test ./framework/configstore/tables/ -run 'TestRateLimit|TestValidateAllowedIPs|TestHash'` → compile error / FAIL.
- [ ] **Step 3: Implement** fields, validation, hash writes, migration entries (nullable columns, no backfill).
- [ ] **Step 4: Run to verify pass** — same command → PASS. Then `go build ./...` from `framework/` and `transports/` module dirs.
- [ ] **Step 5: Commit** — `feat(governance): add ip limit fields to rate limits and allowed-ips to virtual keys`

### Task 2: config.json schema + HTTP API + holder-kind gate

**Files:**
- Modify: `transports/config.schema.json` — `rate_limit_line` gains `ip_max_limit` (integer) + `ip_reset_duration` (string); the top-level `rate_limits` items block (~line 1225) gains the same pair; the `virtual_keys` items block (~line 1562) gains `allowed_ips` (array of strings, unique, format-documented as IP or CIDR). Note: `rate_limit_line` is referenced by access profiles too — the fields ride along (schema accepts them) but enforcement never reads them for non-VK holders.
- Modify: `transports/bifrost-http/handlers/governance.go` — `CreateRateLimitRequest`/`UpdateRateLimitRequest` gain `IPMaxLimit *int64` + `IPResetDuration *string`; `applyRateLimitUpdate` extends with the IP pair (nil max clears duration, mirroring token/request); the VK rate-limit creation sites (~line 1300, ~line 3735) copy the new fields; `CreateVirtualKeyRequest`/`UpdateVirtualKeyRequest` gain `AllowedIPs []string` (update: `schemas.OptionalJSON[[]string]`-style tri-state, absent = keep, `[]`/null = clear); team/customer/provider-config create+update handlers reject `ip_max_limit != nil` / `ip_reset_duration != nil` / `allowed_ips` with a validation error.
- Test: existing governance handler test files (locate with `grep -l "CreateVirtualKey\|UpdateTeam" transports/bifrost-http/handlers/*_test.go`)

**Interfaces:**
- Consumes: Task 1's table fields and `ValidateAllowedIPs`.
- Produces: API accepts on VK bodies: `rate_limit.ip_max_limit`, `rate_limit.ip_reset_duration`, top-level `allowed_ips`; returns 400 on them for team/customer/provider bodies.

- [ ] **Step 1: Write failing tests** — VK create with `ip_max_limit: 5, ip_reset_duration: "1h", allowed_ips: ["10.0.0.0/24"]` persists all three (read back through the store); team create with `rate_limit.ip_max_limit` → 400 naming the field; update tri-state: omitting `allowed_ips` keeps stored value, sending `[]` clears it; `allowed_ips: ["nonsense"]` → 400.
- [ ] **Step 2: Run to verify fail** — `go test ./transports/bifrost-http/handlers/ -run 'TestVirtualKey|TestTeam'` → FAIL.
- [ ] **Step 3: Implement** schema + handlers + gate.
- [ ] **Step 4: Run to verify pass** — same command → PASS.
- [ ] **Step 5: Commit** — `feat(governance): expose ip limit and allowed-ips on virtual key api`

### Task 3: Governance enforcement — allowed list + unique-IP window

**Files:**
- Modify: `plugins/governance/store.go` — IP window manager (new unexported type on `LocalGovernanceStore`: `sync.Map` keyed by rate-limit ID → `*ipWindow{mu, ips map[normIP]struct{}, start time.Time}`); `CheckRateLimit` gains the IP check for VK-held limits only (filter: only evaluate when the limit was gathered under `LimitHolderVirtualKey`); `UpsertRateLimitConfig` carries the window per the spec's update semantics (raise keep / duration change re-anchor / clear drop); `DeleteRateLimit` drops the record; `rebuildInMemoryStructures` clears all windows.
- Modify: `plugins/governance/resolver.go` — `evaluateAccess`: after the listing gate, resolve the request's VK via `PresentedVirtualKey(ctx)` + `GetVirtualKey`, and when its `AllowedIPs` is non-empty, match the normalized context IP (precompiled at load — store a parsed form beside the VK, e.g. a `sync.Map` vkID → `[]net.IPNet` maintained in `storeVirtualKey`/`DeleteVirtualKeyInMemory`); outside → `DecisionAccessBlocked`. `evaluateLimits`: pass the IP into the rate-limit check.
- Modify: `plugins/governance/store.go` `CheckRateLimit` signature — keep the existing signature; read the IP from the context (`bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyClientIP)`) inside the store so no interface changes.
- Test: existing `plugins/governance/` test files (`permits_test.go` for access decisions, `ratelimitreset_test.go` for window behavior)

**Interfaces:**
- Consumes: Task 1 fields; `BifrostContextKeyClientIP`; `PresentedVirtualKey`; existing `CheckRateLimit`/`evaluateAccess` flow.
- Produces: enforcement behavior — 403 on list violation, 429 on IP-limit violation, atomic check-and-insert, lazy window expiry, per-holder gating.

- [ ] **Step 1: Write failing tests** — (a) VK with `allowed_ips` admits a listed IP and CIDR member, refuses others with `DecisionAccessBlocked` and a reason that does not enumerate the list; (b) normalization cases from Review Focus 3; (c) unresolvable IP + allowed list → blocked; unresolvable + only limit → allowed (other limits still checked); (d) unique-IP limit: N distinct IPs pass, N+1th refused with `DecisionRateLimited`; same IP repeats pass without consuming slots; window roll admits again; (e) concurrent boundary (Review Focus 4): `IPMaxLimit=2`, 5 goroutines with 5 distinct IPs → exactly 2 admitted; (f) team-held limit with (seeded) IP fields is never IP-checked; (g) update semantics: raise keeps set, duration change re-anchors, clear drops.
- [ ] **Step 2: Run to verify fail** — `go test ./plugins/governance/ -run 'TestIP|TestAllowedIPs|TestUniqueIP'` → FAIL.
- [ ] **Step 3: Implement.** Keep the check inside `CheckRateLimit` reading the context; no `GovernanceStore` interface changes.
- [ ] **Step 4: Run to verify pass** — same command → PASS, then `go test ./plugins/governance/` full package.
- [ ] **Step 5: Benchmark** — add `BenchmarkCheckRateLimitWithIP` next to the existing benchmarks in the package; record ns/op + allocs/op before/after in the PR body.
- [ ] **Step 6: Commit** — `feat(governance): enforce virtual key ip allowlist and unique-ip window`

### Task 4: Capture gate — resolve IP for enforcement

**Files:**
- Modify: `transports/bifrost-http/lib/config.go` — `HandlerStore` interface gains `ShouldEnforceClientIP() bool`; `Config` implements it over a new atomic-bool field; add the setter the server uses.
- Modify: `transports/bifrost-http/lib/ctx.go:873` — condition becomes `store.ShouldCaptureClientIP() || store.ShouldEnforceClientIP()`.
- Modify: `transports/bifrost-http/server/plugins.go` (governance case) and the VK/rate-limit mutation handlers — after every governance reload/mutation listed in the spec, recompute the flag from the store and push it (`atomic.Bool.Store`).
- Test: `transports/bifrost-http/lib/ctx_test.go` (existing `TestConvertToBifrostContext_ClientIPOffByDefault` file)

**Interfaces:**
- Consumes: Task 3's store state (a method on `LocalGovernanceStore` like `HasClientIPControls() bool`, wired through `GovernanceInMemoryStore`/plugin as the server already does for other cross-layer reads).
- Produces: `ShouldEnforceClientIP() bool` on `HandlerStore`; every test `HandlerStore` mock in the repo gains the method (mechanical; the compiler enumerates them).

- [ ] **Step 1: Write failing tests** — ctx test: enforcement on + capture off + XFF present → IP in context; both off → absent (existing test keeps passing); flag false until a VK with `allowed_ips` is stored, true after, false after delete (store-level test in `plugins/governance`).
- [ ] **Step 2: Run to verify fail** — `go test ./transports/bifrost-http/lib/ -run TestConvertToBifrostContext` → FAIL.
- [ ] **Step 3: Implement** interface method, `Config` impl, ctx widening, recompute at each mutation point (enumerate them in a comment at the setter).
- [ ] **Step 4: Run to verify pass** — same command + `go build ./...` across modules; fix every mock the compiler names.
- [ ] **Step 5: Commit** — `feat(transport): resolve client ip for ip-control enforcement independent of log capture`

### Task 5: UI — VK sheet fields and details display

**Files:**
- Modify: `ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx` — zod schema gains `ipMaxLimit` + `ipResetDuration` beside the token/request pair (line ~141, ~703, ~979, ~1379, ~1478); the rate-limiting block (~line 1886) gains a third `NumberAndSelect` for maximum IPs; an `allowed_ips` string-list input (textarea or chip input reusing an existing list-input component — search `ui/components` for an existing tag/chip list before building one); `data-testid`: `vk-ip-max-limit-input`, `vk-ip-reset-duration-select`, `vk-allowed-ips-input`.
- Modify: `ui/app/workspace/virtual-keys/views/virtualKeyDetailsSheet.tsx` — display the two values in the existing rate-limit block.
- Modify: `ui/locales/en/governance.json` (+ `models.json` if the label keys live there — match where `maximumTokens`/`maximumRequests` live) + all 10 locale dirs with real translations.
- Test: `ui/app/workspace/virtual-keys/views/virtualKeySheet.utils.test.ts` (payload mapping), vitest.

**Interfaces:**
- Consumes: Task 2's API fields.
- Produces: UI reads/writes `ip_max_limit`, `ip_reset_duration`, `allowed_ips` on the VK payload only.

- [ ] **Step 1: Write failing test** — payload mapping includes the IP fields when set and omits them when unset; allowed-ips input splits on newlines/commas and rejects an unparseable entry inline (zod message, lazy via `msg()`).
- [ ] **Step 2: Run to verify fail** — `cd ui && npx vitest run virtualKeySheet.utils.test.ts` → FAIL.
- [ ] **Step 3: Implement** form fields, details display, i18n keys (one whole sentence per key).
- [ ] **Step 4: Run to verify pass** — vitest + `npm run i18n:check` + `npm run format:changed` + `npm run build` all exit 0.
- [ ] **Step 5: Commit** — `feat(ui): virtual key ip limit and allowed ip list fields`

### Task 6: Provider-harness regression case

**Files:**
- Modify: `tests/e2e/api/collections/provider-harness.json` — surgical splice (script, never whole-file reserialize; ~50k lines)

**Interfaces:**
- Consumes: Tasks 2–4 end-to-end.
- Produces: wire-pinned cases, red before the change / green after, validated structurally without a live run.

- [ ] **Step 1: Write the cases** — following `.claude/skills/harness-test-writer/SKILL.md`: (a) VK with allowed list, request from a listed IP header (`X-Forwarded-For`) → 2xx; unlisted IP header → 403 with the generic reason; (b) VK with `ip_max_limit: 1`, two distinct XFF IPs → first 2xx, second 429; (c) allowed list + no IP headers → 403. Assert exact statuses; never early-return on error codes; include `pm.response.text()` in failure messages. Use a dedicated test VK created and deleted by the collection's own setup/teardown pattern.
- [ ] **Step 2: Validate structurally** — `node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json` then `node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "ip limit"` → cases present, no schema errors.
- [ ] **Step 3: Commit** — `test(harness): pin virtual key ip control wire behavior`

### Task 7: Docs + full verification

**Files:**
- Modify: `docs/` — the virtual-key feature doc (locate with `grep -rl "request_max_limit" docs/`) gains the two fields in its config.json/API tabs; `docs/openapi/openapi.json` gains the new request fields.
- Modify: `plugins/governance/changelog.md` if the repo convention adds entries there.

- [ ] **Step 1: Update docs** to match the schema exactly.
- [ ] **Step 2: Full verification** — `go test ./framework/configstore/... ./plugins/governance/ ./transports/bifrost-http/lib/ ./transports/bifrost-http/handlers/` (governance + transport packages; `make test-governance` where wired), `cd ui && npm run i18n:check && npm run build`.
- [ ] **Step 3: Commit** — `docs: virtual key ip controls`

## Out of scope (do not build)

- Cross-instance IP-set sharing or persistence (documented limitation in the spec).
- IP controls on teams, customers, access profiles, or provider-level limits.
- XFF trust hardening (documented risk in the spec's Constraints).
