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

- [x] **Task 3b: Unique-IP window (CheckRateLimits)** — done, commit `9762c6355`
- [x] **Task 4: Capture gate — resolve IP for enforcement** — done, commit `36a7b21da6`
  - Acceptance: ctx sets IP when capture **or** enforcement is on, absent when both off (`TestConvertToBifrostContext_ClientIPForEnforcement`); store flag false → true on VK with IP controls → false on delete/clear; recompute on every B12 point — VK create/update/delete, rate-limit upsert/delete, boot + config.json seed (hook registration pushes current state), plugin re-instantiation (`Init`/`InitFromStore` via `governance.Config.ClientIPControlsChanged`, wired in `server/plugins.go`) — one test per path in `plugins/governance/ipcapturegate_test.go`; all 3 `HandlerStore` mocks updated; logging plugin untouched (no change under `plugins/logging`, enforcement never persists IPs).
  - Notes: `lib.Config` carries an `atomic.Bool` behind `ShouldEnforceClientIP`/`SetEnforceClientIP` (new `HandlerStore` interface method); `ctx.go` condition widened to `capture || enforce`. The store computes the answer incrementally: `allowedIPLists.present` (existing) + a new `ipLimitedVKs` counter moved by O(1) deltas at VK create/update/delete; `refreshClientIPControls` (full rescan) runs only at rebuild and rate-limit upsert/delete rows that could matter. First draft rescanned per write and hung `TestBudgetResetAccuracyAt100kKeysAllDue` (O(n²) config load) — caught by the suite, redesigned to counters, scale test back to 1.5s.
  - Verify: `go test ./transports/bifrost-http/lib/ -run TestConvertToBifrostContext` red (missing method) → green; full `lib`, `plugins/governance` (26s), `framework/configstore/...`, targeted `handlers` + `server` suites green; IP suites green under `-race`; `go build ./bifrost-http/{lib,handlers,server}` clean. Full `handlers` suite is SIGKILLed by the environment (no test failures; documented in SPEC §0 / todo Task 2).
  - Files: `lib/config.go` (interface :106, impl :8644), `lib/ctx.go` (:877), `server/plugins.go` (:299), `plugins/governance/store.go`, `plugins/governance/main.go`; tests: `lib/ctx_test.go`, `plugins/governance/ipcapturegate_test.go` (new), 3 mock updates.
  - Commit: `feat(transport): resolve client ip for ip-control enforcement independent of log capture`

## Checkpoint 2 — enforcement complete

- [ ] Full `go test ./plugins/governance/` green; benchmark numbers recorded
- [ ] `Evaluate` end-to-end behaves per B7/B9 for list + limit together
- [ ] Human review before UI/harness

---

## Phase 3: Surfaces — UI and wire pinning

- [x] **Task 5: UI — VK sheet fields and details display** — done
  - Acceptance: payload includes `ip_max_limit`/`ip_reset_duration`/`allowed_ips` when set, omits when unset; list input splits on newlines/commas with inline lazy zod rejection; existing list/chip component reused if one fits (no new dependency without asking); `data-testid` `vk-ip-max-limit-input`, `vk-ip-reset-duration-select`, `vk-allowed-ips-input`; details sheet shows values; all strings i18n'd in 11 locales.
  - Notes: no list/chip input in `ui/components` fits a free-form IP list (the chip components are model/provider pickers bound to server data), so the field is a `Textarea` split on newlines/commas — no dependency added. Parsing/validation lives in `virtualKeySheet.utils.ts` (`parseAllowedIPsInput`, `invalidAllowedIPEntries`, `allowedIPsCreate`, `allowedIPsUpdate`, `ipRateLimitFields`) so it is unit-tested; `invalidAllowedIPEntries` mirrors `ValidateAllowedIPs` (v4/v6, CIDR prefix bounds, no ports/zones/whitespace). `ipRateLimitFields` is overloaded by body kind: update sends `null`/`null` to clear, create omits. Payload mapping goes through the helpers rather than growing the inline token/request blocks. `NumberAndSelect` gained an optional `selectDataTestId` prop (backwards-compatible) to carry `vk-ip-reset-duration-select`. Details sheet shows the IP window as a summary card (distinct-IP count has no usage gauge) and lists `allowed_ips` as monospace badges in basic information. IP-percent-used in `GetBudgetAndRateLimitStatus`: **not added** — the only consumer is routing's candidate scoring, which compares budget/token/request percentages; a distinct-IP set has no accumulate-then-reset usage to express as a percentage, so a field no consumer reads would be dead weight. The plan's default is overridden for that reason and recorded here.
  - Verify: `npx vitest run virtualKeySheet.utils.test.ts` red (17 failures, functions missing) → green; full `npx vitest run` 164 files / 2457 tests green; `npm run i18n:check` exit 0 (all 10 non-English catalogs `ok`); `npm run format:changed` + `format:changed:check` exit 0; `npx tsc --noEmit` exit 0 with `app/routeTree.gen.ts` generated by the build (without it the tree reports 123 pre-existing errors that also reproduce on the parent commit); `vite build` succeeds (needs `NODE_OPTIONS=--max-old-space-size=4096` on this 7 GB box).
  - Files: `virtualKeySheet.tsx`, `virtualKeyDetailsSheet.tsx`, `virtualKeySheet.utils.ts`(+test), `ui/lib/types/governance.ts`, `ui/components/ui/numberAndSelect.tsx`, 11 `models.json` catalogs.
  - E2E (repo rule: every ui/ change): page object gained `ipMaxLimit`/`ipResetDuration` on `RateLimitConfig` and `allowedIPs` on `VirtualKeyConfig` plus setters; `virtual-keys-creation.spec.ts` gained a "Client IP Controls" block (limit round-trip, allowlist round-trip, inline rejection) — 4/4 green against a live server on this tree. Full `virtual-keys` suite exposed two PRE-EXISTING failures in `virtual-keys-listed-models.spec.ts`, both fixed here: (1) the spec clicked `getByTestId('vk-listed-models-model-select').getByRole('combobox')` but that testid sits on the combobox trigger itself (ModelSelector:438), so the descendant search could never match; (2) the round-trip test sits outside the `describe` (closes at line 69) so it never got the `beforeEach` `goto()` and clicked into a blank page. Fixed locators/navigation, both tests green; suite rerun 5/5 on the feature filter.
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

- [x] IP-percent-used in `GetBudgetAndRateLimitStatus`? **Resolved in Task 5: not added.** The only consumer is routing's candidate scoring (budget/token/request percent); a distinct-IP set has no accumulate-then-reset usage, so the field would be dead weight.
- [x] Allowed-IPs input: **Textarea**, newline/comma split. No `ui/components` list input fits a free-form IP list and no dependency was added.
- [ ] Refusal reason wording — builder's, pinned by Task 3a/3b tests.