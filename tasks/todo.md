# Task List: Virtual Key Client-IP Controls

Plan: `tasks/plan.md` · Contract: `SPEC.md` · Design: `docs/superpowers/specs/2026-11-17-virtual-key-ip-controls-design.md`

Working agreement: `source .github/workflows/scripts/setup-go-workspace.sh` before any Go command.

---

## Phase 1: Foundation — storage and API contract

- [x] **Task 1: Tables, validation, hashes, migration** — done, commit `b8bb8ffe6f`
  - Acceptance: `BeforeSave` rejects missing/zero/negative/unparsable IP pair and accepts a valid one; `ValidateAllowedIPs` accepts v4/v6/CIDR and rejects `10.0.0.0/33`, `999.1.1.1`, `""`, untrimmed whitespace; rate-limit hash covers IP pair unconditionally, VK hash only when `AllowedIPs != nil`; nullable columns via `addColumnIfNotExists`, no backfill.
  - Verify: `go test ./framework/configstore/tables/ -run 'TestRateLimit|TestValidateAllowedIPs|TestHash'` (red → green); `go build ./...` in `framework/` and `transports/`.
  - Files: `framework/configstore/tables/ratelimit.go`, `tables/virtualkey.go`, `framework/configstore/clientconfig.go`, `framework/configstore/migrations.go`; tests in new `tables/ratelimit_test.go` + existing `tables/virtualkey_test.go` + clientconfig tests.
  - Commit: `feat(governance): add ip limit fields to rate limits and allowed-ips to virtual keys`

- [x] **Task 2: config.json schema, HTTP API, holder-kind gate** — done, commits `f25facc33` (schema + VK fields), `7e1b5ee8b` (holder gate + sync paths), `6f9d26baf` (tests)
  - Notes: schema blocks updated by name (`rate_limit_line`, top-level `rate_limits` items, `virtual_keys` items); `allowed_ips` is `schemas.OptionalJSON[[]string]` tri-state on update, null/[] both clear (stored as NULL via `ClearAllowedIPs`, mirroring `ClearListedModels`); holder gate refuses the fields by name on team/customer/model-config/provider-governance create+update AND on the VK's own provider_configs/model_budgets tiers; VK create/update pre-validate the rate limit before the transaction so a bad pair is a 400, not a 500; `rateLimitFromRequestFields` gained the IP pair (nil-nil at non-VK tiers, `req.RateLimit.IP*` at the top level); `TestConfigSchemaSync` green.
  - Verify: `/tmp/handlers.test -test.run 'TestVirtualKeyIPControls|TestNonVirtualKeyHoldersRejectIPControlFields'` red → green (5/5); full handlers suite green except 29 pre-existing BifrostMCP/ClaudeCodeGateway failures that reproduce on a clean checkout of the parent commit (spec path + tmpfs-disk-full artifacts of this environment, unrelated to this change).
  - Commit: `feat(governance): expose ip limit and allowed-ips on virtual key api`

## Checkpoint 1 — contract frozen

- [ ] Tasks 1–2 green; config.json examples validate against the schema
- [ ] Two commits landed with the messages above
- [ ] Human review before enforcement work

---

## Phase 2: Enforcement — governance and capture gate

- [x] **Task 3a: Allowed-IP list enforcement (evaluateAccess)** — done, commits `dcb56e27e7`, `c91900647c`
  - Acceptance: listed exact IP and CIDR member admitted; outsider, missing IP and unparseable IP → `DecisionAccessBlocked` with `"Client IP is not allowed for this virtual key"` (does not enumerate the list); normalization `2001:0db8::1` ≡ `2001:db8::1`, `::ffff:1.2.3.4` matches its v4 CIDR, port/brackets/zone/whitespace stripped, garbage unresolvable; no list configured leaves the request untouched; CIDRs compiled once per load and never per request.
  - Notes: compiled lists live in `allowedIPsCache` on `LocalGovernanceStore`, keyed by VK row ID, so `GovernanceStore` is unchanged (B6). The gate reads the row ID `ResolvePermits` already stamped, so the no-list case is one map miss and no key walk; a store without the cache falls back to compiling on the spot. Cache maintained by `storeVirtualKey`, `DeleteVirtualKeyInMemory` (outside the range loop, so an already-evicted row still drops its entry) and `rebuildInMemoryStructures`. `allowedIPsCache.present`/`any()` is the hook Task 4's capture gate will read.
  - Cost: `BenchmarkEvaluateAccessWithAllowedIPs` — no list 779.5 ns/op, 832 B/op, **18 allocs/op**, identical to the same measurement with the gate disabled (18); list configured 906.5 ns/op, 848 B/op, 19 allocs/op (the request-IP parse).
  - Verify: `go test ./plugins/governance/ -run 'TestAllowedIPs|TestIPNormal'` 39 cases green (red-first confirmed by compile failure, then by two mutations — membership and pointer reuse — each failing the intended assertion); full `go test ./plugins/governance/` green; new tests green under `-race`.
  - Files: `plugins/governance/resolver.go`, `store.go`, new `ipenforce.go`; tests in new `allowedips_test.go` (the package already had per-source test files but no file for the IP path; the resolver/store behavior is covered there rather than splitting into `permits_test.go`/`resolver_test.go`).

- [ ] **Task 3b: Unique-IP window (CheckRateLimits)**
  - Acceptance: N distinct pass, N+1th → `DecisionRateLimited` naming count/max/duration; repeat IP free; window roll admits again; concurrency `IPMaxLimit=2` + 5 goroutines/5 IPs → exactly 2 admitted; unresolvable + limit-only → allowed, other limits enforced; team-held and global-provider limits never IP-checked; upsert raise keeps / duration change re-anchors / clear drops; delete drops; rebuild clears; recording only at admission; `BenchmarkCheckRateLimitWithIP` recorded before/after.
  - Verify: `go test ./plugins/governance/ -run 'TestUniqueIP|TestIPWindow'` (red → green); full package `-race` for the boundary test; `go test -bench BenchmarkCheckRateLimitWithIP -benchmem`.
  - Files: `plugins/governance/store.go` (`CheckRateLimits` :5311, `UpsertRateLimitConfig` :563, `DeleteRateLimit` :592, `rebuildInMemoryStructures` :3598); tests: `ratelimitreset_test.go`, `permits_test.go`.
  - Commit (3a+3b): `feat(governance): enforce virtual key ip allowlist and unique-ip window`

- [ ] **Task 4: Capture gate — resolve IP for enforcement**
  - Acceptance: ctx sets IP when capture **or** enforcement is on, absent when both off; store flag false → true on VK with IP controls → false on delete; recompute on every B12 point (VK create/update/delete, rate-limit upsert/delete, config sync boot + file, plugin re-instantiation) with one test per path; all `HandlerStore` mocks updated; logging plugin untouched.
  - Verify: `go test ./transports/bifrost-http/lib/ -run TestConvertToBifrostContext` (red → green); `go build ./bifrost-http/lib/ ./bifrost-http/handlers/ ./bifrost-http/server/`.
  - Files: `lib/config.go` (:104 interface, :8630 impl), `lib/ctx.go` (:873), `server/plugins.go`, `handlers/governance.go` mutation paths; tests: `lib/ctx_test.go`, governance store test.
  - Commit: `feat(transport): resolve client ip for ip-control enforcement independent of log capture`

## Checkpoint 2 — enforcement complete

- [ ] Full `go test ./plugins/governance/` green; benchmark numbers recorded
- [ ] `Evaluate` end-to-end behaves per B7/B9 for list + limit together
- [ ] Human review before UI/harness

---

## Phase 3: Surfaces — UI and wire pinning

- [ ] **Task 5: UI — VK sheet fields and details display**
  - Acceptance: payload includes `ip_max_limit`/`ip_reset_duration`/`allowed_ips` when set, omits when unset; list input splits on newlines/commas with inline lazy zod rejection; existing list/chip component reused if one fits (no new dependency without asking); `data-testid` `vk-ip-max-limit-input`, `vk-ip-reset-duration-select`, `vk-allowed-ips-input`; details sheet shows values; all strings i18n'd in 11 locales.
  - Verify: `cd ui && npx vitest run virtualKeySheet.utils.test.ts` (red → green); `npm run i18n:check`; `npm run format:changed`; `npm run build`.
  - Files: `ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx`, `virtualKeyDetailsSheet.tsx`, `virtualKeySheet.utils.ts`, locale files.
  - Commit: `feat(ui): virtual key ip limit and allowed ip list fields`

- [ ] **Task 6: Provider-harness regression cases**
  - Acceptance: (a) listed XFF → 2xx, unlisted → 403 generic reason; (b) `ip_max_limit: 1` two distinct XFF IPs → 2xx then 429; (c) allowed list + no IP headers → 403; exact statuses, no early-return, `pm.response.text()` in failures, dedicated VK via collection setup/teardown; script-spliced only.
  - Verify: `node tests/e2e/api/runners/augment-provider-harness.mjs …`; `node tests/e2e/api/runners/filter-collection.mjs … --feature "ip limit"` shows the cases.
  - Files: `tests/e2e/api/collections/provider-harness.json` (+ throwaway splice script, deleted after).
  - Commit: `test(harness): pin virtual key ip control wire behavior`

## Checkpoint 3 — wire contract pinned

- [ ] Structural validation clean; cases under the `ip limit` filter
- [ ] Human review before docs

---

## Phase 4: Documentation and full verification

- [ ] **Task 7: Docs + full verification**
  - Acceptance: virtual-keys/budget-and-limits/config-json docs describe the fields matching the schema; `docs/openapi/openapi.json` updated; changelog entry if convention applies.
  - Verify: `go test ./framework/configstore/... ./plugins/governance/ ./transports/bifrost-http/lib/ ./transports/bifrost-http/handlers/`; `cd ui && npm run i18n:check && npm run build`; final report ends with the two operator handover commands.
  - Files: docs + openapi + `plugins/governance/changelog.md`.
  - Commit: `docs: virtual key ip controls`

## Checkpoint: Complete

- [ ] All SPEC.md §8 success criteria met
- [ ] Handover commands ready for the operator's live harness run

---

## Open questions (blocking nothing yet)

- [ ] IP-percent-used in `GetBudgetAndRateLimitStatus`? Default yes; lands with Task 5 if approved.
- [ ] Allowed-IPs input: reuse vs textarea — decided in Task 5 after searching `ui/components`.
- [ ] Refusal reason wording — builder's, pinned by Task 3a/3b tests.