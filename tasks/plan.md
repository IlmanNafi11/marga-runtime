# Implementation Plan: Virtual Key Client-IP Controls

- Date: 2026-11-17
- Spec: `SPEC.md` (root) — the contract; behavior rules B1–B14 are the acceptance criteria source.
- Source plan: `docs/superpowers/plans/2026-11-17-virtual-key-ip-controls.md` (operator-approved); this file refines it with code-verified anchors and execution mechanics. Where they disagree, SPEC.md wins, then this file.
- Working agreement: `source .github/workflows/scripts/setup-go-workspace.sh` before any Go command in a fresh checkout.

## Overview

Two virtual-key-level client-IP controls — a unique-IP limit over a rolling reset window (429) and an explicit allowed-IP/CIDR list (403) — enforced by the governance plugin on every governed pipeline, configurable through config.json, the governance HTTP API, and the UI VK sheet. Zero added cost for keys without IP config; enforcement never persists IPs to logs.

## Architecture decisions

- **Storage rides `TableRateLimit`** (approach A of the design): `IPMaxLimit`/`IPResetDuration` join token/request on the same row; `AllowedIPs` is a JSON column on `TableVirtualKey` (access property of the key, not of a window).
- **Enforcement lives in the `Evaluate` funnel**: allowed list in `evaluateAccess` (resolver.go), IP window in the store's rate-limit check. **Verified planning correction:** `evaluateLimits` calls `CheckRateLimits` (store.go:5311), which is the only point where each `schemas.Limit` (carrying `HolderKind`, `framework/grant/limit.go:30`) is paired with its loaded `*TableRateLimit` before delegation to `CheckRateLimit` (store.go:2215, `EntityWiseRateLimits` map — holder kind lost). The IP-window check is implemented in **`CheckRateLimits`**; `CheckRateLimit` and the `GovernanceStore` interface stay untouched (B6).
- **Holder filter is strict `grant.LimitHolderVirtualKey`.** Consequence (planning decision D2): the VK **provider-config** rate-limit creation site also builds `CreateRateLimitRequest`; it must **reject** `ip_max_limit`/`ip_reset_duration` with a 400 like team/customer/provider bodies do. Accepting them there would store config that the strict filter never enforces — the silent-drop anti-pattern B5 forbids. Provider-config and model-config limits are therefore never IP-checked, and never carry IP fields.
- **Capture gate is an atomic bool on `Config`**, widened condition at `lib/ctx.go:873` (`ShouldCaptureClientIP() || ShouldEnforceClientIP()`), recomputed at every enumerated mutation point (B12). `ClientForwardedIP(ctx)` + `ctx.RemoteIP()` fallback already exist at that site; no new resolution code.
- **CIDRs compile once at load**: parsed `[]net.IPNet` kept beside the VK in store memory (`storeVirtualKey` store.go:2136 / `DeleteVirtualKeyInMemory` interface :283 maintain it), never parsed per request (B8).
- **Why layered tasks, not per-knob vertical slices:** both knobs share the same table row, the same `BeforeSave` pattern, the same hash functions, the same schema blocks, and the same UI block. A per-knob slice would run two red/green cycles over the identical files and double the merge surface. Slicing is vertical *within* tasks (each task lands storage→test→verification of its own fields); phases group the dependency order.

## Task list

### Phase 1: Foundation — storage and API contract

#### Task 1: Tables, validation, hashes, migration

**Description:** Add the IP pair to `TableRateLimit` (+`BeforeSave` validation mirroring token/request exactly), `AllowedIPs` + `ValidateAllowedIPs` to `TableVirtualKey`, extend both hash functions, add nullable columns via the migration chain.

**Acceptance criteria:**
- [ ] `BeforeSave` rejects: `IPMaxLimit` without `IPResetDuration`; zero/negative max; unparsable or non-positive duration. Accepts a valid pair.
- [ ] `ValidateAllowedIPs` accepts `10.0.0.1`, `2001:db8::1`, `10.0.0.0/24`, `2001:db8::/32`; rejects `10.0.0.0/33`, `999.1.1.1`, `""`, `" 10.0.0.1"` (untrimmed whitespace rejected — normalization happens at enforcement, never storage).
- [ ] `GenerateRateLimitHash` changes when either IP field is set/changed; `GenerateVirtualKeyHash` changes only when `AllowedIPs != nil` (pre-existing rows keep their hash).
- [ ] Migration adds nullable columns, no backfill.

**Verification:**
- [ ] Red first: `go test ./framework/configstore/tables/ -run 'TestRateLimit|TestValidateAllowedIPs|TestHash'` fails for the assertion's reason.
- [ ] Green after; `go build ./...` clean in `framework/` and `transports/` module dirs.

**Dependencies:** None. **Files:** `framework/configstore/tables/ratelimit.go`, `tables/virtualkey.go`, `framework/configstore/clientconfig.go` (`GenerateRateLimitHash` :1878, `GenerateVirtualKeyHash` :1638), `framework/configstore/migrations.go`. Tests: **new** `tables/ratelimit_test.go` (source file untested today — sanctioned exception), existing `tables/virtualkey_test.go`, existing clientconfig test file. **Size:** M (4 files + tests).

#### Task 2: config.json schema, HTTP API, holder-kind gate

**Description:** Update `transports/config.schema.json` first (source of truth), then the governance handler request structs and the VK-only acceptance gate.

**Acceptance criteria:**
- [ ] Schema: `$defs/rate_limit_line` (**:13804** — the plan's ~1225/~1562 anchors are stale; locate the `rate_limits` and `virtual_keys` items blocks by name) gains `ip_max_limit` (integer), `ip_reset_duration` (string); `virtual_keys` items gain `allowed_ips` (array of unique strings, IP-or-CIDR documented).
- [ ] VK create persists `rate_limit.ip_max_limit`, `rate_limit.ip_reset_duration`, top-level `allowed_ips` (read back through the store); `allowed_ips: ["nonsense"]` → 400.
- [ ] Update tri-state: omitting `allowed_ips` keeps the stored value; `[]`/null clears it (`schemas.OptionalJSON[[]string]`, pattern already at governance.go:468).
- [ ] Team, customer, provider-config, **and VK provider-config** create/update bodies carrying any IP field → 400 naming the field.

**Verification:**
- [ ] Red first: `go test ./transports/bifrost-http/handlers/ -run 'TestVirtualKey|TestTeam'` fails.
- [ ] Green after; package builds (`go build ./bifrost-http/handlers/`).

**Dependencies:** Task 1. **Files:** `transports/config.schema.json`, `transports/bifrost-http/handlers/governance.go` (`CreateVirtualKeyRequest` :371, `UpdateVirtualKeyRequest` :449, `CreateRateLimitRequest` :620, `UpdateRateLimitRequest` :628, `applyRateLimitUpdate`, VK rate-limit creation sites). Tests: `handlers/governance_test.go`, `governanceroutes_test.go`. **Size:** M (2 files + tests).

### Checkpoint 1 — contract frozen
- [ ] Tables + hashes + schema + handler gate all green; schema validates against config.json examples.
- [ ] Commit boundary: `feat(governance): add ip limit fields…`, `feat(governance): expose ip limit and allowed-ips…`.
- [ ] Review with human before enforcement work starts (the refusal semantics get locked here).

### Phase 2: Enforcement — governance and capture gate

#### Task 3a: Allowed-IP list enforcement (evaluateAccess)

**Description:** Normalization helper, parsed-CIDR cache beside the VK in store memory, allowed-list gate in `evaluateAccess` after the existing gates.

**Acceptance criteria:**
- [ ] Listed exact IP and CIDR member admitted; outsider → `DecisionAccessBlocked` (403), reason does not enumerate the list.
- [ ] Normalization: `2001:0db8::1` ≡ `2001:db8::1`; `::ffff:1.2.3.4` matches its v4 CIDR; port suffix, zone, whitespace stripped; garbage = unresolvable. No raw string comparison anywhere.
- [ ] Unresolvable IP + allowed list → blocked (fail closed).
- [ ] CIDRs compiled once at load (`storeVirtualKey`/`DeleteVirtualKeyInMemory` maintain the cache), zero per-request parsing.

**Verification:**
- [ ] Red first: `go test ./plugins/governance/ -run 'TestAllowedIPs|TestIPNormal'` fails.
- [ ] Green after; full `go test ./plugins/governance/` still green.

**Dependencies:** Tasks 1–2. **Files:** `plugins/governance/resolver.go` (`evaluateAccess` :98), `store.go` (`storeVirtualKey` :2136, `GetVirtualKey` :1250). Tests: `permits_test.go`, `resolver_test.go`. **Size:** M.

#### Task 3b: Unique-IP window (CheckRateLimits)

**Description:** In-memory window manager keyed by limit ID; atomic check-and-insert at admission inside `CheckRateLimits` (holder kind available there — see Architecture decisions); update/delete/rebuild semantics.

**Acceptance criteria:**
- [ ] N distinct IPs pass, N+1th → `DecisionRateLimited` (429) with reason naming count/max/duration; repeat IP consumes no slot; window roll admits again (lazy expiry, no ticker, rolling only).
- [ ] Concurrency: `IPMaxLimit=2`, 5 goroutines / 5 distinct IPs → exactly 2 admitted (one atomic step under the record mutex).
- [ ] Unresolvable IP + limit-only → allowed (fail open), other limits still checked.
- [ ] Team-held limit with seeded IP fields never IP-checked (B14); `GlobalProviderLimits` path (limits==nil in `evaluateLimits`) likewise never.
- [ ] `UpsertRateLimitConfig` (:563): raise/unchanged keeps set; duration change re-anchors empty; clear drops record. `DeleteRateLimit` (:592) drops; `rebuildInMemoryStructures` (:3598) clears all.
- [ ] Recording happens only at admission — never in `BumpRateLimitUsage`/`ReportUsage`.
- [ ] `BenchmarkCheckRateLimitWithIP` lands; before/after ns/op + allocs/op recorded for the PR.

**Verification:**
- [ ] Red first: `go test ./plugins/governance/ -run 'TestUniqueIP|TestIPWindow'` fails.
- [ ] Green after + full package green + benchmark runs with `-benchmem`.

**Dependencies:** Tasks 1–2 (3a independent of 3b; both touch store.go — land 3a first). **Files:** `plugins/governance/store.go`, `resolver.go` (pass-through only if needed). Tests: `ratelimitreset_test.go`, `permits_test.go`. **Size:** L (2 source files but dense state machine; single focused session).

#### Task 4: Capture gate — resolve IP for enforcement

**Description:** `ShouldEnforceClientIP()` on `HandlerStore` over an atomic bool; widen the ctx condition; recompute at every mutation point.

**Acceptance criteria:**
- [ ] `ConvertToBifrostContext` sets the IP when capture **or** enforcement is on; both off → absent (existing `TestConvertToBifrostContext_ClientIPOffByDefault` keeps passing).
- [ ] Store-level `HasClientIPControls()` (or equivalent on the plugin/in-memory store): false → true when a VK with IP controls is stored → false after delete.
- [ ] Recompute fires on **every** B12 point: VK create/update/delete, rate-limit upsert/delete, config.json sync (boot + file-driven), plugin re-instantiation (`server/plugins.go`) — one test per path.
- [ ] Every test `HandlerStore` mock gains the method (compiler enumerates).
- [ ] Logging plugin untouched — enforcement never persists IPs.

**Verification:**
- [ ] Red first: `go test ./transports/bifrost-http/lib/ -run TestConvertToBifrostContext` fails.
- [ ] Green after + `go build ./bifrost-http/lib/ ./bifrost-http/handlers/ ./bifrost-http/server/` clean (full-module build blocked only by the pre-existing missing `ui/build` embed — see SPEC.md §0).

**Dependencies:** Task 3b (flag source). **Files:** `lib/config.go` (:104 interface, :8630 impl), `lib/ctx.go` (:873), `server/plugins.go`, VK/rate-limit mutation handlers in `handlers/governance.go`. Tests: `lib/ctx_test.go`, governance store test. **Size:** M (4–5 files, mostly mechanical).

### Checkpoint 2 — enforcement complete
- [ ] Full `go test ./plugins/governance/` green; benchmark numbers recorded in the task notes.
- [ ] Unit-level end-to-end: VK with list + limit behaves per B7/B9 through `Evaluate`.
- [ ] Review with human before UI/harness (wire semantics now observable).

### Phase 3: Surfaces — UI and wire pinning

#### Task 5: UI — VK sheet fields and details display

**Description:** Third rate-limit row (`NumberAndSelect`) + allowed-IPs list input in the VK sheet; details display; i18n across all 11 locales.

**Acceptance criteria:**
- [ ] Payload mapping includes `ip_max_limit`, `ip_reset_duration`, `allowed_ips` when set, omits when unset.
- [ ] List input splits on newlines/commas; unparseable entry rejected inline (lazy zod message via `msg()`); reuses an existing list/chip component if one fits (`ui/components` searched first — no new dependency without asking).
- [ ] `data-testid`: `vk-ip-max-limit-input`, `vk-ip-reset-duration-select`, `vk-allowed-ips-input`.
- [ ] Details sheet shows the values in the existing rate-limit block.
- [ ] All new strings i18n'd, real translations in all 10 non-English locales.

**Verification:**
- [ ] Red first: `cd ui && npx vitest run virtualKeySheet.utils.test.ts` fails.
- [ ] Green after + `npm run i18n:check` + `npm run format:changed` + `npm run build` all exit 0.

**Dependencies:** Task 2 (API fields). **Files:** `ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx` (labels at :1891/:1917), `virtualKeyDetailsSheet.tsx`, `virtualKeySheet.utils.ts`, locale files. **Size:** M.

#### Task 6: Provider-harness regression cases

**Description:** Surgical splice of wire-pinned cases into `tests/e2e/api/collections/provider-harness.json`.

**Acceptance criteria:**
- [ ] (a) listed XFF → 2xx; unlisted → 403 generic reason; (b) `ip_max_limit: 1`, two distinct XFF IPs → 2xx then 429; (c) allowed list + no IP headers → 403.
- [ ] Exact statuses asserted; no early-return on any error code; `pm.response.text()` in failure messages; dedicated VK via the collection's own setup/teardown pattern.
- [ ] Spliced by script, never whole-file reserialized.

**Verification:**
- [ ] `node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json` clean.
- [ ] `node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "ip limit"` shows the new cases.

**Dependencies:** Tasks 2–4. **Files:** `tests/e2e/api/collections/provider-harness.json` (+ a throwaway splice script, deleted after). **Size:** S.

### Checkpoint 3 — wire contract pinned
- [ ] Structural validation passes; cases present under the `ip limit` feature filter.
- [ ] Review with human before docs/final verification.

### Phase 4: Documentation and full verification

#### Task 7: Docs + full verification

**Description:** Docs match the schema exactly; OpenAPI updated; full verification sweep.

**Acceptance criteria:**
- [ ] `docs/features/governance/virtual-keys.mdx`, `budget-and-limits.mdx`, `docs/deployment-guides/config-json/governance.mdx` document the fields in config.json/API tabs.
- [ ] `docs/openapi/openapi.json` gains the new request fields.
- [ ] `plugins/governance/changelog.md` entry if repo convention adds one.

**Verification:**
- [ ] `go test ./framework/configstore/... ./plugins/governance/ ./transports/bifrost-http/lib/ ./transports/bifrost-http/handlers/` green.
- [ ] `cd ui && npm run i18n:check && npm run build` green.
- [ ] Final report ends with the two operator handover commands (placeholders substituted).

**Dependencies:** All. **Files:** docs + openapi + changelog. **Size:** S.

### Checkpoint: Complete
- [ ] All SPEC.md §8 success criteria met; ready for the operator's live harness run.

## Risks and mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Missed `ShouldEnforceClientIP` recompute point | High: with capture off, a newly configured key 403s everything until restart | One test per enumerated mutation path (Task 4); points listed in a comment at the setter |
| IP fields accepted at the VK provider-config site but never enforced | High: silent config drop, B5 violation | Task 2 gate covers that site explicitly; strict holder filter documented |
| Concurrent boundary race | High: two IPs both pass a full set | Atomic check-and-insert under the record mutex; goroutine test at the boundary (Task 3b) |
| `CheckRateLimit` vs `CheckRateLimits` confusion | Med: check lands where holder kind is lost | Decided up front: check in `CheckRateLimits`; verified this turn |
| Harness splice corrupts the 50k-line collection | Med: review noise, broken CI | Script-splice only; structural validation before commit |
| UI embed build failure mistaken for a regression | Low: wasted debug time | Baseline documented in SPEC.md §0; package-scoped builds in every task |

## Open questions

1. IP-percent-used in `GetBudgetAndRateLimitStatus` — default yes (SPEC.md §9.1); lands with Task 5 if approved.
2. Allowed-IPs input: reuse vs textarea — builder decides in Task 5 after searching `ui/components`.
3. Refusal reason strings — builder's wording, pinned by Task 3a/3b tests.

## Parallelization

Tasks 3a and 3b are independent of each other but both edit `plugins/governance/store.go` — land 3a first, or run in separate worktrees. Tasks 5 and 6 are parallelizable after Checkpoint 2 (different trees, no shared files). Everything else is sequential.
