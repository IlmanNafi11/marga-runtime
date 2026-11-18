# Virtual Key Client-IP Controls — Design

Date: 2026-11-17
Status: approved in conversation, builder check incorporated, pending operator approval

## Intent and why

Virtual keys today can be capped on tokens and requests (`rate_limits` block: `token_max_limit` / `request_max_limit` with reset durations). Operators hosting keys for many users have no way to constrain *where* requests come from, even though Bifrost already resolves and logs the client IP (`logs_store.capture_client_ip`, `BifrostContextKeyClientIP`).

Two new controls, both at virtual-key level:

1. **Unique-IP limit** — cap the number of distinct client IPs that may use a key, counted over a reset window exactly like the token and request counters. Unset = unlimited.
2. **Allowed-IP list** — an explicit list of IPs/CIDRs; when set, only listed IPs may use the key, everything else is refused. Unset = rule #1 applies alone.

Both rules apply independently when both are configured: an IP inside the allowed list still consumes a unique-IP slot and can be refused by the limit.

## Goals, non-goals, anti-goals

**Goals**
- Configuration through the same surfaces operators already use: `config.json` (schema), the governance HTTP API, and the UI rate-limiting block.
- Zero added cost for keys without IP configuration (nil checks only).
- Consistent refusal semantics with existing governance decisions.

**Non-goals**
- Cross-instance sharing of the unique-IP set (multi-instance deployments count per instance; see Constraints).
- Persisting the unique-IP set to the database (restart re-counts within the open window).
- Geographic, ASN, or reputation filtering; per-user IP attribution beyond the requesting client IP.

**Anti-goals (failure modes)**
- Silently weakening `capture_client_ip` privacy: enforcement must not start persisting IPs to logs.
- A request whose IP cannot be determined slipping past an allowed list.

## Constraints

- `BifrostContextKeyClientIP` is populated only when `logs_store.capture_client_ip` is on (`lib/ctx.go`, pinned by `TestConvertToBifrostContext_ClientIPOffByDefault`). Enforcement cannot depend on that flag; see IP resolution below.
- Rate limits are shared by virtual keys, teams, customers, and provider configs (`TableRateLimit` has one row shape for all holders). The IP fields ride that one row, but the feature is **virtual-key scoped**: see Virtual-key scoping below.
- Client IP order is fixed by existing behavior: leftmost `X-Forwarded-For`, else `X-Real-IP`, else TCP peer (`ClientForwardedIP`). A spoofable XFF is an accepted, documented risk of that order; direct (unproxied) exposure lets callers claim any XFF. Documented, not fixed here.

## Approaches considered

**A. Extend `TableRateLimit` with IP fields** — `ip_max_limit` + `ip_reset_duration` join token/request on the same row; validation, config.json sync, reset semantics, UI block, and status reporting follow the existing pattern. *Chosen.* The windowing machine already exists and the operator framing puts this in the rate-limiting block; a parallel structure would rebuild it for no gain.

**B. Separate IP-control block on the virtual key** (own table/fields, own enforcement path) — semantically cleaner separation of access control from quotas, but duplicates the reset-duration/sync/UI machinery and forks the mental model. Rejected.

**Allowed-IP list placement** — a JSON column on `TableVirtualKey` (`allowed_ips`), not on `TableRateLimit`: the list has no counter or window, it is an access property of the key itself, and a VK's rate limit may live at provider level while the list must apply to the whole key.

## Decisions made in conversation

- Unique-IP counting follows the reset window (not all-time): e.g. "max 5 IPs per 1h" admits a 6th distinct IP once the window rolls.
- Allowed list and IP limit compose (both enforced), per the operator's answer above.
- Limit storage: approach A. List storage: my call — `TableVirtualKey` column, entries validated as single IPs or CIDRs at create/update.
- Refusal semantics: allowed-list violation → `DecisionAccessBlocked` (403, access refusal). Unique-IP limit exceeded → `DecisionRateLimited` (429, quota refusal). No new `Decision` values, so no new error-type declarations.
- Enforcement point: the governance `Evaluate` funnel (`evaluateAccess` / `evaluateLimits`), which every governed pipeline shares — LLM requests via `PreLLMHook`, and MCP tool execution, relays, and A2A via their own pre-hooks. The allowed list and the unique-IP limit therefore bind every surface a virtual key governs, matching "only the IPs you set may use this virtual key".
- IP resolution for enforcement: when any loaded virtual key carries IP controls, the client IP is resolved into the context even when `capture_client_ip` is off. Keys without IP config keep today's behavior exactly (no IP in context, no cost).

## Virtual-key scoping (operator decision)

The unique-IP limit is offered on **virtual keys only**. Teams, customers, and deployment-level provider limits do not get IP controls in this version.

- `CreateRateLimitRequest` / `UpdateRateLimitRequest` and the `rate_limit_line` schema entry are shared by every holder kind. The VK create/update path accepts `ip_max_limit`; the team, customer, and provider-config paths reject it with a validation error rather than silently dropping it.
- Enforcement follows the holder: a rate limit gathered for a non-VK holder never evaluates its IP window (its IP fields are always nil by construction).
- Rationale: the request names virtual keys, and a team-level "distinct IPs" count is a different product semantic (all keys behind the team pooled together) that is easy for an operator to misread.

## Design

### Configuration surface

`TableRateLimit` (framework/configstore/tables/ratelimit.go) gains:

- `IPMaxLimit *int64` — maximum distinct client IPs per window; nil = unlimited.
- `IPResetDuration *string` — same duration grammar as token/request (`30s`…`1Y`); required when `IPMaxLimit` is set (mirrors existing `BeforeSave` validation, including positivity).

`TableVirtualKey` gains:

- `AllowedIPs []string` (`gorm:"type:text;serializer:json"`) — validated entries: single IPv4/IPv6 or CIDR. Empty/nil = feature off. Validation helper rejects unparseable entries at create/update and at config.json load.

HTTP API (`CreateRateLimitRequest` / `UpdateRateLimitRequest`, VK create/update bodies): `ip_max_limit`, `ip_reset_duration` on the rate limit; `allowed_ips` on the virtual key. Update semantics follow the existing pointer tri-state (nil = keep, `[]`/null clears).

`transports/config.schema.json` is updated first (source of truth), then handlers, then docs.

### IP resolution and the capture gate

`ConvertToBifrostContext` sets `BifrostContextKeyClientIP` under a widened condition: `ShouldCaptureClientIP() || ShouldEnforceClientIP()`. `ShouldEnforceClientIP` is a new `HandlerStore` method backed by an atomic bool mirrored from the governance plugin's state (per-request plugin lookup would not be free). It is true when any loaded virtual key carries IP controls — a non-empty allowed list, or a rate limit with `IPMaxLimit` set — and is recomputed on **every** mutation path that can change that: VK create/update/delete, rate limit upsert/delete, config.json sync (boot and file-driven), and plugin re-instantiation (`server/plugins.go`). A missed recompute would combine with `capture_client_ip` off and the fail-closed rule below into a 403 for every request to the key until restart, so the recompute points are enumerated, not left to a catch-all. The existing default-off test gains a case for the enforcement-driven path; the logging plugin's persistence keeps following `capture_client_ip` alone, so enforcement never writes IPs to logs.

When no usable IP can be resolved — no headers, no peer, or a value that does not parse after normalization (see below):

- Allowed list configured → **refuse** (fail closed; membership cannot be proven).
- Only the unique-IP limit configured → skip the IP check (cannot attribute), other limits still enforced.

### Unique-IP tracking

A new in-memory manager in the governance store (not on `TableRateLimit`, not gossiped, not persisted):

- `sync.Map` keyed by rate-limit ID → window record: mutex, `map[ip]struct{}`, window start.
- Check and record (`evaluateLimits` path, at admission): resolve and normalize the IP; a member of the open window passes; a new IP joins the set when `len(set) < IPMaxLimit` (check-and-insert is one atomic step under the record's mutex, so two concurrent first requests from different IPs cannot both squeeze past the limit); when the IP is new and the set is full → `DecisionRateLimited`, reason names count/max/duration. Recording at admission, not at the post-request usage bump, is deliberate: `BumpRateLimitUsage` runs on the post-hook worker's base context without the request's IP (`UsageUpdate` carries none), and threading it through would break the `GovernanceStore` interface for every implementation; batch settlement (`ReportUsage`) and delta-folding bumps therefore never record IPs, and this path is the only writer.

- Window expiry: lazy — on access, if `now - windowStart >= IPResetDuration`, clear the set and re-anchor. No ticker. Calendar alignment does not apply to the IP counter (rolling windows only) in this version.
- Update semantics on `UpsertRateLimitConfig` (the IP window is store state keyed by limit ID, carried across config replacement):
  - `IPMaxLimit` raised or unchanged → the open window's set is kept.
  - `IPResetDuration` changed → the window re-anchors to now with an empty set (the token/request precedent of carrying state forward would let a shortened window stay anchored in the past).
  - `IPMaxLimit` cleared to nil → the record is dropped immediately.
- Instance restart clears the set; the window restarts empty. Multi-instance deployments count per instance (documented limitation).

### Enforcement

- `evaluateAccess` (resolver.go): after the model/listing gates, if the request's VK (resolved from the context's virtual-key value, the store's existing lookup) has a non-empty allowed list, the parsed-at-load CIDR set decides membership. Outside → `DecisionAccessBlocked` with a reason that does not enumerate the list ("Client IP is not allowed for this virtual key").
- The IP value is normalized before any comparison: strip whitespace, strip port/zone, `net.ParseIP`; unparseable counts as unresolvable. Normalization makes `2001:0db8::1` and `2001:db8::1` one IP and lets `::ffff:1.2.3.4` match its v4 CIDR. Raw string comparison would be a spoof-shaped bypass and is not used.
- `evaluateLimits`: `CheckRateLimits` additionally evaluates every gathered rate limit's IP window as above, alongside token/request checks.
- Parsed CIDR forms are compiled once when the VK/limit is loaded into the store, never per request.

### Cost statement

- Keys without IP config: one nil check on the VK's allowed list and two nil fields on each gathered rate limit — zero allocations, no header reads (the capture gate is one bool).
- Keys with IP config: one header peek / peer read (already benchmarked shape: `BenchmarkConvertToBifrostContextClientIP`), one map lookup per gathered IP-enabled limit, one map insert per admitted request. A `Benchmark` for the governance-side check lands with the change, run with `-benchmem`, before/after numbers in the PR.
- Retained memory: one window record per IP-enabled rate limit, bounded by its `IPMaxLimit` (the set stops growing at the limit; refused IPs are not recorded), freed when the window rolls or the limit is deleted (`DeleteRateLimit` drops the record).

### Testing

- Unit: `BeforeSave` validation (duration pairing, positivity), allowed-IP parsing (v4, v6, CIDR, rejects), enforcement decisions in the existing governance test files (list block 403, limit 429, both compose, unresolvable-IP behavior, window roll, update tri-state), capture-gate cases in `lib/ctx_test.go`.
- Provider-harness case in `tests/e2e/api/collections/provider-harness.json` (wire-visible: 403/429 responses), red before / green after, validated via the augment/filter scripts.
- UI: the VK rate-limiting block gains the two fields; `data-testid` for new inputs; E2E coverage per repo rules; i18n for all new strings.

### What is left to the builder

- Exact reason-string wording; UI layout within the existing rate-limiting block; whether `GetBudgetAndRateLimitStatus` reports an IP-percent-used field (cheap, useful for the UI gauge — builder's call).
- The `rate_limit_line` schema entry and the shared rate-limit request structs gain the IP fields once; the holder-kind gate (VK only) lives in the VK/team/customer/provider handlers' validation, and the UI shows the IP fields only in the virtual-key form.
- Migration: new columns via the existing migration chain (`addColumnIfNotExists` pattern), nullable, no backfill needed.
