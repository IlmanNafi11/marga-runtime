# Spec: Virtual Key Client-IP Controls (IP Limitation)

- Date: 2026-11-17
- Status: drafted from approved design + plan; awaiting operator approval (no implementation until approved)
- Repository: `IlmanNafi11/bifrost`, worktree `add-ip-limit`
- Source documents: `docs/superpowers/specs/2026-11-17-virtual-key-ip-controls-design.md` (design, approved in conversation), `docs/superpowers/plans/2026-11-17-virtual-key-ip-controls.md` (implementation plan)
- This file is the contract the plan implements. Where the plan and this spec disagree, this spec wins until it is amended. The design doc remains the record of *why*; this file states *what*.

---

## 0. Scope check (Phase 0)

The request names one feature — virtual-key IP controls — delivered as two knobs (unique-IP limit, allowed-IP list) that share every piece of machinery: the same `TableRateLimit` row pattern, the same governance `Evaluate` funnel, the same capture gate, the same UI rate-limiting block, the same harness cases. Neither knob ships or verifies alone: an allowed list without the capture-gate work is unenforceable, and the unique-IP limit reuses the identical window/reset/validation machinery. Cutting one would not leave an independently shippable capability behind.

**Determination: single capability. No capability map; one spec.** The plan's Tasks 1–7 are dependency-ordered slices within this one spec, not modules.

The previous root spec (`SPEC.md`, Marga runtime release) belonged to a shipped initiative (v0.0.1 stable recorded in git history); this file replaces it as the active contract. The Marga spec remains recoverable in git history; its design/plan docs stay under `docs/superpowers/`.

### Assumptions I am making (correct now, or they become defects)

1. The approved design doc is final; this spec turns it into testable contract language, not a re-design.
2. Virtual-key scoping only — teams, customers, access profiles, and provider-level limits get no IP controls in this version (design decision, restated as boundary).
3. XFF spoofing is an accepted, documented risk; the leftmost-XFF resolution order is not changed here.
4. The live provider harness run is the operator's; the agent ships unit tests + structural harness validation and hands over the two commands.

- Verified baseline (this worktree): `go.work` is gitignored and absent until `source .github/workflows/scripts/setup-go-workspace.sh` runs; after that, `framework/configstore` builds clean. `transports/bifrost-http` fails to build only because the embedded-UI embed (`all:ui`) has no `ui/build` output in a fresh worktree — pre-existing, unrelated to this feature; the packages this spec touches (`lib/`, `handlers/`, `server/`) compile as packages (`go build ./bifrost-http/lib/ ./bifrost-http/handlers/ ./bifrost-http/server/`). CONSTRAINTS.md's stale "configstore does not build" note does not apply here.

## 1. Objective

Operators hosting virtual keys for many users can today cap tokens and requests but cannot constrain *where* requests come from, even though Bifrost already resolves the client IP. Two virtual-key-level controls close this:

1. **Unique-IP limit** (`ip_max_limit` + `ip_reset_duration` on the rate limit) — at most N distinct client IPs per rolling reset window; the N+1th distinct IP is refused with 429 until the window rolls. Unset = unlimited.
2. **Allowed-IP list** (`allowed_ips` on the virtual key) — when set, only listed IPs/CIDRs may use the key; everything else is refused with 403. Unset = rule 1 alone.

Both configured → both enforced independently: a listed IP still consumes a unique-IP slot and can be refused by the limit.

**Users:** the operator (configures via config.json / governance API / UI VK sheet); the API consumer (feels 403/429 with consistent governance refusal semantics).

**Success looks like:** a VK with `allowed_ips: ["10.0.0.0/24"]` serves 10.0.0.5 and refuses 10.1.0.5 with 403; a VK with `ip_max_limit: 2` over `1h` admits the first two distinct IPs and 429s the third, then admits again after the window rolls; a key with no IP config behaves byte-identically to today (zero added cost); no IP ever reaches the logs because of enforcement.

---

## 2. Behavior contract (normative)

These rules are the acceptance criteria; each maps to tests in §6.

**B1 Storage.** `TableRateLimit` gains `IPMaxLimit *int64` (`ip_max_limit,omitempty`) and `IPResetDuration *string` (`ip_reset_duration,omitempty`); `BeforeSave` mirrors the token/request rules exactly: duration required when max is set, duration parses and is positive, max is positive. `TableVirtualKey` gains `AllowedIPs []string` (`gorm:"type:text;serializer:json"`, `allowed_ips,omitempty`), validated by `ValidateAllowedIPs` (single IPv4/IPv6 via `net.ParseIP`, CIDR via `net.ParseCIDR`; rejects unparseable, empty strings, and untrimmed whitespace — normalization happens at enforcement, never at storage).

**B2 Hashes.** `GenerateRateLimitHash` incorporates the IP pair unconditionally (matches its existing per-field style). `GenerateVirtualKeyHash` incorporates `AllowedIPs` only when non-nil, mirroring the `ListedModels` presence rule so pre-existing rows keep their hash and config sync sees no drift on upgrade.

**B3 Migration.** New columns via the existing `addColumnIfNotExists` chain; nullable; no backfill.

**B4 Schema first.** `transports/config.schema.json` is updated before handlers: `rate_limit_line` gains the IP pair; the top-level `rate_limits` items block gains the same pair; the `virtual_keys` items block gains `allowed_ips` (array of unique strings, IP-or-CIDR). The fields ride along in `rate_limit_line` refs used by access profiles (schema accepts, enforcement never reads them for non-VK holders).

**B5 API surface + holder gate.** `CreateRateLimitRequest`/`UpdateRateLimitRequest` gain the IP pair; `applyRateLimitUpdate` extends with it (nil max clears duration, mirroring token/request). VK create/update bodies accept `rate_limit.ip_max_limit`, `rate_limit.ip_reset_duration`, and top-level `allowed_ips` (update tri-state: absent = keep, `[]`/null = clear). Team, customer, and provider-config create/update handlers **reject** `ip_max_limit`/`ip_reset_duration`/`allowed_ips` with a 400 naming the field — never silently drop.

**B6 Enforcement point.** The governance `Evaluate` funnel: `evaluateAccess` checks the VK's allowed list after the existing gates (resolve VK via `PresentedVirtualKey` + store lookup; CIDRs compiled once at load, never per request); `evaluateLimits` evaluates every gathered rate limit's IP window alongside token/request checks. The IP check inside the store reads the IP from context (`BifrostContextKeyClientIP`) — **no `GovernanceStore` interface change, no signature change**. Note: both `CheckRateLimit` (store.go:2215) and `CheckRateLimits` (store.go:5311) exist; the check lands on the path `evaluateLimits` actually drives — the builder verifies which and keeps them consistent.

**B7 Decisions.** No new `Decision` values. Allowed-list violation → `DecisionAccessBlocked` (403) with a reason that does not enumerate the list ("Client IP is not allowed for this virtual key"). Unique-IP limit exceeded → `DecisionRateLimited` (429) with a reason naming count/max/duration.

**B8 Normalization.** Before any comparison: trim whitespace, strip port/zone, `net.ParseIP`; unparseable = unresolvable. This makes `2001:0db8::1` ≡ `2001:db8::1` and lets `::ffff:1.2.3.4` match its v4 CIDR. Raw string comparison is forbidden (spoof-shaped bypass).

**B9 Unresolvable IP.** Allowed list configured → refuse (fail closed). Only the unique-IP limit configured → skip the IP check (fail open); other limits still enforced.

**B10 Unique-IP window.** In-memory manager in the governance store: `sync.Map` keyed by rate-limit ID → `{mutex, map[normalizedIP]struct{}, windowStart}`. Check-and-insert is one atomic step under the record mutex (two concurrent first requests from different IPs cannot both pass a full set). Recording happens at admission, the only writer — never in `BumpRateLimitUsage`/`ReportUsage`. Lazy expiry: on access, `now - windowStart >= duration` clears the set and re-anchors; no ticker; rolling windows only (no calendar alignment for the IP counter in this version). Refused IPs are not recorded; the set stops growing at `IPMaxLimit` (retained-memory bound).

**B11 Window update semantics** (`UpsertRateLimitConfig`): max raised or unchanged → keep the open set; duration changed → re-anchor to now with an empty set; max cleared to nil → drop the record immediately. `DeleteRateLimit` drops the record; `rebuildInMemoryStructures` clears all windows.

**B12 Capture gate.** `ConvertToBifrostContext` sets `BifrostContextKeyClientIP` when `ShouldCaptureClientIP() || ShouldEnforceClientIP()`. `ShouldEnforceClientIP()` is a new `HandlerStore` method backed by an atomic bool, true iff any loaded VK carries IP controls (non-empty allowed list or rate limit with `IPMaxLimit` set), recomputed on **every** mutation path that can change it: VK create/update/delete, rate-limit upsert/delete, config.json sync (boot + file-driven), plugin re-instantiation (`server/plugins.go`). The logging plugin keeps following `capture_client_ip` alone — enforcement never persists IPs.

**B13 Zero cost for unconfigured keys.** One nil check on the VK list, two nil fields per gathered limit, one bool for the capture gate; zero allocations, no header reads. `BenchmarkCheckRateLimitWithIP` lands with the change (`-benchmem`, before/after in the PR).

**B14 Non-VK holders.** A rate limit gathered for a non-VK holder is never IP-checked (its IP fields are nil by construction; the check additionally filters on `LimitHolderVirtualKey`).

---

## 3. Commands

All from repo root. `source .github/workflows/scripts/setup-go-workspace.sh` first in a fresh checkout (verified: `go.work` is gitignored and absent until sourced; after sourcing, `framework` and `transports` build clean on this branch — the stale "configstore does not build" note in CONSTRAINTS.md does not apply here).

```bash
# Task 1 — tables/validation/hashes (red → green)
go test ./framework/configstore/tables/ -run 'TestRateLimit|TestValidateAllowedIPs|TestHash'
go build ./...   # from framework/ and transports/ module dirs

# Task 2 — schema + handlers + holder gate
go test ./transports/bifrost-http/handlers/ -run 'TestVirtualKey|TestTeam'

# Task 3 — governance enforcement + benchmark
go test ./plugins/governance/ -run 'TestIP|TestAllowedIPs|TestUniqueIP'
go test ./plugins/governance/            # full package
go test ./plugins/governance/ -bench BenchmarkCheckRateLimitWithIP -benchmem

# Task 4 — capture gate
go test ./transports/bifrost-http/lib/ -run TestConvertToBifrostContext

# Task 5 — UI
cd ui && npx vitest run virtualKeySheet.utils.test.ts
cd ui && npm run i18n:check && npm run format:changed && npm run build

# Task 6 — harness structural validation (no live run)
node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json
node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "ip limit"

# Task 7 — full verification
go test ./framework/configstore/... ./plugins/governance/ ./transports/bifrost-http/lib/ ./transports/bifrost-http/handlers/
cd ui && npm run i18n:check && npm run build
```

Handover to the operator (agent never runs these):

```bash
make dev PORT=<port> APP_DIR=$(pwd)/tests/integrations/python
make run-provider-harness-test PROVIDER=<provider> FEATURE="ip limit" BASE_URL=http://localhost:<port>
```

---

## 4. Project structure

All paths verified to exist in this worktree. Test files listed are the existing files tests land in.

| Task | Modify | Tests land in |
|---|---|---|
| 1 | `framework/configstore/tables/ratelimit.go`, `virtualkey.go`, `framework/configstore/clientconfig.go`, `framework/configstore/migrations.go` | `tables/virtualkey_test.go` (exists); `tables/ratelimit_test.go` is **new** — `ratelimit.go` has no test file today, and a file named after its source is the sanctioned exception; hash tests in the existing clientconfig test file |
| 2 | `transports/config.schema.json` (`$defs/rate_limit_line` at :13804 + the `rate_limits` and `virtual_keys` items blocks — the plan's ~1225/~1562 line anchors are stale; locate by name), `transports/bifrost-http/handlers/governance.go` | `handlers/governance_test.go`, `governanceroutes_test.go` |
| 3 | `plugins/governance/store.go`, `resolver.go` | `permits_test.go`, `ratelimitreset_test.go`, `resolver_test.go` |
| 4 | `transports/bifrost-http/lib/config.go` (HandlerStore :104, impl :8630), `lib/ctx.go` (:873), `transports/bifrost-http/server/plugins.go`, VK/rate-limit mutation handlers | `lib/ctx_test.go`; store-level flag test in `plugins/governance` |
| 5 | `ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx` (labels at :1891/:1917; zod + rate-limiting block), `virtualKeyDetailsSheet.tsx`, `ui/locales/en/…` + all 10 other locale dirs | `virtualKeySheet.utils.test.ts` |
| 6 | `tests/e2e/api/collections/provider-harness.json` (surgical splice only) | validated by the two runner scripts |
| 7 | `docs/features/governance/virtual-keys.mdx`, `budget-and-limits.mdx`, `docs/deployment-guides/config-json/governance.mdx`, `docs/openapi/openapi.json`, `plugins/governance/changelog.md` (exists) | — |

---

## 5. Code style

Inherited from `AGENTS.md`/`CONSTRAINTS.md`, restated where this change touches:

- Go: no underscore filenames; error strings lowercase, no trailing punctuation; struct field comments are short trailing one-liners — rationale lives in the function doc comment (the window manager's admission-vs-post-hook reasoning goes on the manager type, not inline).
- JSON tags `snake_case`; pointer tri-state on updates (nil = keep, zero/`[]` = clear) exactly as token/request and `ListedModels` do today.
- Converters/validators pure; no logging or HTTP in table validation.
- UI: every user-visible string through i18next, one whole sentence per key, real translations in all 11 locales; `data-testid="vk-ip-max-limit-input"`, `vk-ip-reset-duration-select`, `vk-allowed-ips-input` on new inputs; zod messages lazy via `msg()`.
- Harness JSON: splice with a script; never reserialize the 50k-line collection.

```go
// The window record is store state, not request state: keyed by limit ID,
// bounded by IPMaxLimit, freed on delete or window roll.
type ipWindow struct {
    mu    sync.Mutex
    ips   map[string]struct{} // normalized IPs; refused IPs are never inserted
    start time.Time
}
```

---

## 6. Testing strategy

Red before green on every task: the failing test is written first and confirmed to fail for the assertion's reason, not a compile error. Tests go into the existing files listed in §4; the single new file is `tables/ratelimit_test.go` (source file untested today).

**Unit matrix (each row = at least one test):**

| Contract rule | Where |
|---|---|
| B1 duration pairing/positivity, `ValidateAllowedIPs` accept/reject set (`10.0.0.1`, `2001:db8::1`, `10.0.0.0/24`, `2001:db8::/32` vs `10.0.0.0/33`, `999.1.1.1`, `""`, trailing-space entry) | tables |
| B2 hash changes when IP fields change, stable when nil | clientconfig tests |
| B5 VK persists all three fields; team body → 400 naming the field; update tri-state keep/clear; `allowed_ips: ["nonsense"]` → 400 | handlers |
| B6/B7 list admits member + CIDR member, refuses outsider with generic reason; unresolvable + list → blocked; unresolvable + limit-only → allowed | governance |
| B8 normalization: v4-mapped IPv6, port suffix, zone, whitespace, garbage | governance |
| B10 N distinct pass, N+1th → 429; repeat IP consumes no slot; window roll admits again; concurrency: `IPMaxLimit=2`, 5 goroutines/5 IPs → exactly 2 admitted | governance |
| B11 raise keeps set / duration change re-anchors / clear drops | governance |
| B14 team-held limit with seeded IP fields is never IP-checked | governance |
| B12 flag false → true on VK store → false on delete; ctx: enforcement on + capture off + XFF → IP in context; both off → absent | governance + `lib/ctx_test.go` |

**Wire pinning:** harness cases (a) listed XFF → 2xx, unlisted → 403 generic reason; (b) `ip_max_limit: 1`, two distinct XFF IPs → 2xx then 429; (c) allowed list + no IP headers → 403. Exact statuses asserted; no early-return on error codes; `pm.response.text()` in failure messages; dedicated VK created/deleted by the collection's own setup/teardown pattern.

**UI:** payload-mapping test (fields present when set, omitted when unset; list input splits on newlines/commas, inline zod rejection of unparseable entries).

**Exemptions (explicit):** none — this change is wire-visible end to end; every layer gets coverage per the matrix above.

**Coverage ratchet:** touched packages must not fall below their recorded floors (CONSTRAINTS.md); UI changed files ≥ 80%.

---

## 7. Boundaries

**Always**

- Update `transports/config.schema.json` before handlers (B4).
- Keep enforcement virtual-key scoped; non-VK bodies get a 400, never a silent drop (B5).
- Normalize before comparing IPs; compile CIDRs at load, never per request (B8).
- Fail closed on the allowed list, fail open on the limit, when the IP is unresolvable (B9).
- One commit per plan task with the plan's stated messages (`feat(governance): …` ×2, `feat(transport): …`, `feat(ui): …`, `test(harness): …`, `docs: …`).
- Run the narrowest verification for the touched task before committing; end the final report with the two handover commands, placeholders substituted.
- `gofmt`/`goimports` on changed Go files; `npm run format:changed` (never repo-wide) in `ui/`.

**Ask first**

- Adding a new `Decision` value or changing refusal status semantics (B7 pins 403/429).
- Touching `CheckRateLimit`/`CheckRateLimits` signatures or the `GovernanceStore` interface (B6 forbids it; a discovered need is a design change).
- Adding a UI dependency for the allowed-IPs input (search `ui/components` for an existing list/chip input first; the choice between textarea and chip input is builder's unless a dependency is needed).
- Adding an IP-percent-used field to `GetBudgetAndRateLimitStatus` (design leaves it to the builder; default = add it, it is cheap and feeds the UI gauge).
- Any change to the XFF resolution order or `capture_client_ip` semantics.

**Never**

- Persist an IP to logs because of enforcement; the logging plugin follows `capture_client_ip` alone (B12).
- Raw string comparison of IP values (B8).
- Store per-IP sets, buffers, or stream-sized data in `BifrostContext` — window state lives in the store manager keyed by limit ID (B10).
- Silently drop `ip_max_limit`/`allowed_ips` on team/customer/provider bodies (B5).
- Skip or early-return on any HTTP status in a harness test script.
- Weaken CONSTRAINTS.md or a test to make a gate pass.
- Build IP controls for teams, customers, access profiles, or provider-level limits; build cross-instance sharing or DB persistence of the IP set (out of scope, documented limitation).
- Run the live provider harness or spend the operator's money.

---

## 8. Success criteria

1. Every unit test in the §6 matrix exists, went red before its fix, and is green after; the full §3 verification command set exits 0.
2. Harness structural validation passes with the `ip limit` feature filter; cases are spliced, not reserialized.
3. Wire behavior matches B7/B9 exactly: 403 with a non-enumerating reason for list violations and unresolvable-IP-with-list; 429 with count/max/duration for limit violations.
4. A key with no IP config shows zero added allocation in `BenchmarkCheckRateLimitWithIP` before/after numbers (nil-check-only path) and byte-identical behavior with the capture gate off.
5. `ShouldEnforceClientIP` flips on every mutation path listed in B12 — one test per path.
6. UI ships the three fields VK-only, i18n-clean across all 11 locales, with the named `data-testid`s; `npm run i18n:check`, `format:changed:check`, `build`, and vitest all exit 0.
7. Docs and `docs/openapi/openapi.json` match the schema exactly.

---

## 9. Open questions

1. **Status reporting (B13-adjacent):** add an IP-percent-used field to `GetBudgetAndRateLimitStatus`? Default: yes (cheap, feeds the UI gauge). Say so if you want it dropped.
2. **Allowed-IPs input shape:** textarea (newline/comma split) vs existing chip/list component — builder searches `ui/components` first and picks the reuse; no new dependency without asking.
3. **Exact reason strings** for the two refusals — builder's wording, pinned by tests once written.

Implementation does not start until this spec is approved. On approval, the existing plan (`docs/superpowers/plans/2026-11-17-virtual-key-ip-controls.md`) executes task 1 → 7 with the corrections in §4 (test-file placement, stale schema line anchors, `CheckRateLimit` vs `CheckRateLimits` verification) folded in.
