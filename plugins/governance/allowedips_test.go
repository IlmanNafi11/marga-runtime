package governance

import (
	"context"
	"net"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAllowedIPsStore builds a resolver over a store holding one virtual key with the given
// allowed_ips list. Every test in this file drives the same access shape: one provider config
// that allows everything on openai, so the only gate that can refuse is the IP gate.
func newAllowedIPsStore(t *testing.T, allowedIPs []string) (*BudgetResolver, string) {
	t.Helper()
	logger := NewMockLogger()
	vk := buildVirtualKey("vk-ip", "sk-bf-ip", "IP Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	vk.AllowedIPs = allowedIPs

	store, err := NewLocalGovernanceStore(t.Context(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)
	return NewBudgetResolver(store, nil, logger, nil), "sk-bf-ip"
}

// evaluateAccessFromIP drives evaluateAccess the way a request whose context carries the given
// client IP does: the value is set on the context exactly as ConvertToBifrostContext sets it,
// and the access is resolved from the presented key.
func evaluateAccessFromIP(resolver *BudgetResolver, vkValue, clientIP string) *EvaluationResult {
	ctx := resolverCtx(resolver.store, vkValue)
	if clientIP != "" {
		ctx.SetValue(schemas.BifrostContextKeyClientIP, clientIP)
	}
	return resolver.evaluateAccess(ctx, &EvaluationRequest{
		RequestType: schemas.ChatCompletionRequest,
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
	}, ctx.Grant().Access())
}

// TestAllowedIPsEnforcement covers the access-gate behavior B6/B7/B9 pin: a listed exact IP and
// a CIDR member are admitted, an outsider is refused with DecisionAccessBlocked and a reason
// that does not enumerate the list, and an unresolvable IP fails closed.
func TestAllowedIPsEnforcement(t *testing.T) {
	t.Run("listed exact IP is admitted", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.1", "2001:db8::1"})
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "10.0.0.1"))
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "2001:db8::1"))
	})

	t.Run("CIDR member is admitted", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.0/24", "2001:db8::/32"})
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "10.0.0.77"))
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "2001:db8::dead"))
	})

	t.Run("unlisted IP is blocked with a non-enumerating reason", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.1"})
		result := evaluateAccessFromIP(resolver, vkValue, "10.0.0.2")
		assertDecision(t, DecisionAccessBlocked, result)
		assert.Equal(t, "Client IP is not allowed for this virtual key", result.Reason)
	})

	t.Run("missing IP with a configured list is blocked (fail closed)", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.1"})
		result := evaluateAccessFromIP(resolver, vkValue, "")
		assertDecision(t, DecisionAccessBlocked, result)
		assert.Equal(t, "Client IP is not allowed for this virtual key", result.Reason)
	})

	t.Run("unparseable IP with a configured list is blocked (fail closed)", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.1"})
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "999.1.1.1"))
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "not-an-ip"))
	})

	t.Run("no list configured leaves the request unrestricted", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, nil)
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "10.9.9.9"))
		// Even with no IP on the context at all: the rule is off, nothing to enforce.
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, ""))
	})
}

// TestIPNormalization covers B8: every equivalent spelling of one address matches, and the
// normalizer never sees a raw string comparison. Port suffixes, zones and whitespace are
// stripped; garbage parses to nothing (unresolvable, so fail closed under a list).
func TestIPNormalization(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // nil equivalent: want == "" and !ok
		ok    bool
	}{
		{"plain v4", "10.0.0.1", "10.0.0.1", true},
		{"v4 with surrounding whitespace", "  10.0.0.1  ", "10.0.0.1", true},
		{"v4 with port", "10.0.0.1:8080", "10.0.0.1", true},
		{"plain v6", "2001:db8::1", "2001:db8::1", true},
		{"v6 long form equals short form", "2001:0db8::1", "2001:db8::1", true},
		{"v6 with zone", "fe80::1%eth0", "fe80::1", true},
		{"v6 with brackets", "[2001:db8::1]", "2001:db8::1", true},
		{"v6 with port after brackets", "[2001:db8::1]:443", "2001:db8::1", true},
		{"v4-mapped v6", "::ffff:10.0.0.1", "10.0.0.1", true},
		{"v4-mapped v6 long form", "::ffff:a00:1", "10.0.0.1", true},
		{"v4-mapped v6 with zone", "::ffff:10.0.0.1%eth0", "10.0.0.1", true},
		{"garbage", "not-an-ip", "", false},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
		{"two addresses", "10.0.0.1,10.0.0.2", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizeClientIP(tc.input)
			if tc.ok {
				require.True(t, ok, "expected %q to parse", tc.input)
				assert.Equal(t, tc.want, got.String())
			} else {
				assert.False(t, ok, "expected %q to be unresolvable", tc.input)
			}
		})
	}
}

// TestNormalizeClientIPMatchesCIDR pins the v4-mapped rule B8 states: an IPv4-mapped IPv6
// spelling of an address matches that address's v4 CIDR, so a proxy that reports ::ffff:10.0.0.7
// is still a member of 10.0.0.0/24.
func TestNormalizeClientIPMatchesCIDR(t *testing.T) {
	ipNet, err := parseAllowedIPEntry("10.0.0.0/24")
	require.NoError(t, err)

	for _, spelling := range []string{"10.0.0.7", "::ffff:10.0.0.7", "  ::ffff:10.0.0.7  "} {
		ip, ok := NormalizeClientIP(spelling)
		require.True(t, ok, "expected %q to parse", spelling)
		assert.True(t, ipNet.Contains(ip), "expected %q to match 10.0.0.0/24", spelling)
	}

	other, ok := NormalizeClientIP("10.0.1.7")
	require.True(t, ok)
	assert.False(t, ipNet.Contains(other))
}

// TestAllowedIPsNormalizationEquivalence pins the acceptance criteria end to end through the
// access gate: the operator's spellings of one address all behave identically at enforcement.
func TestAllowedIPsNormalizationEquivalence(t *testing.T) {
	t.Run("v6 long form matches short form in the list", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{"2001:db8::1"})
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "2001:0db8::1"))
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "2001:db8::2"))
	})

	t.Run("v4-mapped v6 spelling matches its v4 CIDR entry", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.0/24"})
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "::ffff:10.0.0.9"))
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "::ffff:10.0.1.9"))
	})

	t.Run("CIDR with host bits set is refused at the gate like an unlisted IP", func(t *testing.T) {
		// ValidateAllowedIPs accepts 10.0.0.1/24 as a CIDR (ParseCIDR tolerates host bits).
		// Enforcement must treat the compiled network (10.0.0.0/24) as the rule, not the
		// entry's spelling, so a member of the network is admitted.
		resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.77/32"})
		assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "10.0.0.77"))
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "10.0.0.78"))
	})
}

// TestAllowedIPsAgainstOtherGatesOrdering checks the IP gate runs after the existing access
// gates: a request the provider gate refuses for other reasons still reports that refusal, not
// the IP one, so operators debug the real cause first.
func TestAllowedIPsAgainstOtherGatesOrdering(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk-ip", "sk-bf-ip", "IP Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"*"}),
	}
	vk.AllowedIPs = []string{"10.0.0.1"}

	store, err := NewLocalGovernanceStore(t.Context(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)
	resolver := NewBudgetResolver(store, nil, logger, nil)

	ctx := resolverCtx(store, "sk-bf-ip")
	ctx.SetValue(schemas.BifrostContextKeyClientIP, "10.0.0.2")
	result := resolver.evaluateAccess(ctx, &EvaluationRequest{
		RequestType: schemas.ChatCompletionRequest,
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
	}, ctx.Grant().Access())
	assertDecision(t, DecisionProviderBlocked, result)
}

// TestAllowedIPsSurvivesUpdate pins the CIDR cache maintenance contract: a VK edited through
// UpdateVirtualKeyInMemory recompiles its parsed list, so an IP admitted under the old list and
// refused under the new one flips immediately.
func TestAllowedIPsSurvivesUpdate(t *testing.T) {
	resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.1"})
	assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "10.0.0.1"))

	stored, ok := resolver.store.GetVirtualKey(t.Context(), vkValue)
	require.True(t, ok)
	stored.AllowedIPs = []string{"10.0.0.2"}
	resolver.store.UpdateVirtualKeyInMemory(t.Context(), stored, nil, nil, nil)

	assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "10.0.0.1"))
	assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "10.0.0.2"))
}

// TestAllowedIPsKeylessAccessUnaffected: the IP gate binds the virtual key the request
// authenticated with. A request granted by something that is not a key carries no key whose
// list could apply, so the gate has nothing to enforce there.
func TestAllowedIPsKeylessAccessUnaffected(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk-ip", "sk-bf-ip", "IP Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	vk.AllowedIPs = []string{"10.0.0.1"}

	store, err := NewLocalGovernanceStore(t.Context(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)
	resolver := NewBudgetResolver(store, nil, logger, nil)

	// A permit from another holder, no key presented.
	granted := grant.NewPermit("other", "h1", "Holder", true, false, []schemas.ProviderPermit{{
		Provider:      "openai",
		AllowedModels: []string{"*"},
	}}, nil)
	ctx := keylessAccessCtx(granted)
	ctx.SetValue(schemas.BifrostContextKeyClientIP, "10.9.9.9")

	result := resolver.evaluateAccess(ctx, &EvaluationRequest{
		RequestType: schemas.ChatCompletionRequest,
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
	}, ctx.Grant().Access())
	assertDecision(t, DecisionAllow, result)
}

// TestAllowedIPsDenyAll pins the explicitly empty list: an empty, non-nil allowed_ips denies
// every client IP, including an unresolvable one, and is distinct from nil (the rule is off).
// This is the enforcement half of the deny-all configuration; the flag half is pinned in
// TestAllowedIPsCacheTracksAvailability and TestClientIPControlsFlagTracksVirtualKeyWritePaths.
func TestAllowedIPsDenyAll(t *testing.T) {
	t.Run("explicit empty list refuses every IP", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{})
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "10.0.0.1"))
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "2001:db8::1"))
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, ""))
	})

	t.Run("explicit empty list is a configured rule, not an absent one", func(t *testing.T) {
		resolver, vkValue := newAllowedIPsStore(t, []string{})
		store := resolver.store.(*LocalGovernanceStore)
		_, hasList := store.allowedIPLists.load("vk-ip")
		require.True(t, hasList, "an explicitly empty list must be a configured rule")
		assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "10.9.9.9"))
	})
}

// TestAllowedIPsAndWindowOnOneKey pins the gate ordering through the plugin funnel: the access
// gate (Step 3) runs before the limits (Step 4), so a request refused by the allowlist never
// consumes one of the key's unique-IP slots, and an admitted IP then counts toward the limit.
// Driving PreLLMHook rather than the resolver keeps the ordering assertion on the real sequence.
func TestAllowedIPsAndWindowOnOneKey(t *testing.T) {
	rl := buildIPRateLimit("ip-window-rl", 1, "1h")
	logger := NewMockLogger()
	vk := buildVirtualKeyWithRateLimit("vk-ip-window", "sk-bf-ip-window", "IP Window VK", rl)
	vk.AllowedIPs = []string{"10.0.0.0/24"}
	store, err := NewLocalGovernanceStore(t.Context(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	// A listed IP is admitted and takes the window's single slot.
	admitted := resolverCtx(store, "sk-bf-ip-window")
	admitted.SetValue(schemas.BifrostContextKeyClientIP, "10.0.0.1")
	_, shortCircuit, err := plugin.PreLLMHook(admitted, newChatRequest())
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	// A second distinct listed IP is refused by the unique-IP window.
	second := resolverCtx(store, "sk-bf-ip-window")
	second.SetValue(schemas.BifrostContextKeyClientIP, "10.0.0.2")
	_, shortCircuit, err = plugin.PreLLMHook(second, newChatRequest())
	require.NoError(t, err)
	require.NotNil(t, shortCircuit)

	// An unlisted IP is refused by the allowlist — and refused again and again, each time
	// without consuming a slot: the first admitted IP still holds the window's only one.
	for i := 0; i < 3; i++ {
		refused := resolverCtx(store, "sk-bf-ip-window")
		refused.SetValue(schemas.BifrostContextKeyClientIP, "10.9.9.9")
		_, shortCircuit, err = plugin.PreLLMHook(refused, newChatRequest())
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
	}
	window := store.ipWindows.record("ip-window-rl")
	require.NotNil(t, window)
	window.mu.Lock()
	assert.Len(t, window.ips, 1, "only the admitted IP is in the window")
	assert.Contains(t, window.ips, "10.0.0.1")
	window.mu.Unlock()
}

// BenchmarkEvaluateAccessWithAllowedIPs is the B13 cost statement for the allowlist gate: a key
// with no list must pay one map miss and no key walk, and a key with a list must pay one map hit
// plus one membership walk and no parse. Run with -benchmem.
func BenchmarkEvaluateAccessWithAllowedIPs(b *testing.B) {
	cases := []struct {
		name       string
		allowedIPs []string
	}{
		{"no list configured", nil},
		{"list configured", []string{"10.0.0.0/24", "2001:db8::/32"}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			logger := NewMockLogger()
			vk := buildVirtualKey("vk-bench-ip", "sk-bf-bench-ip", "bench-ip", true)
			vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
				buildProviderConfig("openai", []string{"*"}),
			}
			vk.AllowedIPs = tc.allowedIPs
			store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
			}, nil, nil)
			if err != nil {
				b.Fatal(err)
			}
			resolver := NewBudgetResolver(store, nil, logger, nil)

			ctx := resolverCtx(store, "sk-bf-bench-ip")
			ctx.SetValue(schemas.BifrostContextKeyClientIP, "10.0.0.9")
			access := ctx.Grant().Access()
			req := &EvaluationRequest{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Model: "gpt-4o"}

			b.ReportAllocs()
			for b.Loop() {
				if result := resolver.evaluateAccess(ctx, req, access); result.Decision != DecisionAllow {
					b.Fatal(result.Reason)
				}
			}
		})
	}
}

// TestAllowedIPsCacheTracksAvailability pins the counter the capture gate reads: it is false with
// no key configured, true once a key with a list is stored, and false again after that key is
// deleted or its list cleared.
func TestAllowedIPsCacheTracksAvailability(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if store.allowedIPLists.any() {
		t.Fatal("expected no key to carry a list on an empty store")
	}

	vk := buildVirtualKey("vk-ip-avail", "sk-bf-avail", "avail", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai", []string{"*"})}
	vk.AllowedIPs = []string{"10.0.0.1"}
	store.CreateVirtualKeyInMemory(context.Background(), vk)
	if !store.allowedIPLists.any() {
		t.Fatal("expected a stored key with a list to report availability")
	}

	// An explicit empty list is still a configured rule (deny every IP), so it counts.
	empty := *vk
	empty.AllowedIPs = []string{}
	store.UpdateVirtualKeyInMemory(context.Background(), &empty, nil, nil, nil)
	if !store.allowedIPLists.any() {
		t.Fatal("an empty list is a configured rule and must keep reporting availability")
	}

	cleared := *vk
	cleared.AllowedIPs = nil
	store.UpdateVirtualKeyInMemory(context.Background(), &cleared, nil, nil, nil)
	if store.allowedIPLists.any() {
		t.Fatal("expected a key with no list to drop availability")
	}

	withList := *vk
	withList.AllowedIPs = []string{"10.0.0.1"}
	store.UpdateVirtualKeyInMemory(context.Background(), &withList, nil, nil, nil)
	store.DeleteVirtualKeyInMemory(context.Background(), vk.ID)
	if store.allowedIPLists.any() {
		t.Fatal("expected delete to drop availability")
	}
}

// TestAllowedIPsCompiledOnceAtLoad pins the "compiled once, never per request" half of B8: the
// cached list a request reads is the identical slice across resolutions, so no request parses a
// CIDR. A store that mutated or recompiled per request would hand back a different pointer each
// time.
func TestAllowedIPsCompiledOnceAtLoad(t *testing.T) {
	resolver, vkValue := newAllowedIPsStore(t, []string{"10.0.0.0/24", "2001:db8::/32"})
	local, ok := resolver.store.(*LocalGovernanceStore)
	require.True(t, ok)

	vk, found := local.GetVirtualKey(context.Background(), vkValue)
	require.True(t, found)

	// Read it through the accessor the gate itself calls, so the assertion covers the
	// production path rather than the cache's own method.
	first, hasFirst := compiledAllowedIPsFor(local, vk.ID, resolverCtx(local, vkValue))
	require.True(t, hasFirst)
	for range 100 {
		again, hasAgain := compiledAllowedIPsFor(local, vk.ID, resolverCtx(local, vkValue))
		require.True(t, hasAgain)
		require.Same(t, first, again, "the compiled list must be reused, not rebuilt per request")
	}

	// A publish replaces it as a whole, so the next read sees the new rule.
	vk.AllowedIPs = []string{"192.168.0.0/16"}
	local.UpdateVirtualKeyInMemory(context.Background(), vk, nil, nil, nil)
	updated, hasUpdated := local.allowedIPLists.load(vk.ID)
	require.True(t, hasUpdated)
	require.NotSame(t, first, updated)
	assert.Len(t, updated.nets, 1)

	assertDecision(t, DecisionAllow, evaluateAccessFromIP(resolver, vkValue, "192.168.4.4"))
	assertDecision(t, DecisionAccessBlocked, evaluateAccessFromIP(resolver, vkValue, "10.0.0.4"))
}

// TestAllowedIPsFallbackCompileForNonLocalStore pins the compiledAllowedIPsFor fallback: a store
// that keeps no compiled lists (anything but *LocalGovernanceStore) compiles the presented key's
// entries on the spot, so enforcement still holds against a custom GovernanceStore.
func TestAllowedIPsFallbackCompileForNonLocalStore(t *testing.T) {
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(t.Context(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	vk := buildVirtualKey("vk-fallback", "sk-bf-fallback", "Fallback VK", true)
	vk.AllowedIPs = []string{"10.0.0.0/24"}
	double := &fallbackStoreDouble{GovernanceStore: local, vk: vk}

	ctx := resolverCtx(local, "sk-bf-fallback")
	ctx.SetValue(schemas.BifrostContextKeyClientIP, "10.0.0.5")
	list, hasList := compiledAllowedIPsFor(double, vk.ID, ctx)
	require.True(t, hasList, "the fallback must compile the presented key's entries")
	ip, ok := NormalizeClientIP("10.0.0.5")
	require.True(t, ok)
	assert.True(t, ipAllowedInList(list.nets, ip))

	// A key with no list answers "no rule" through the fallback too.
	vk.AllowedIPs = nil
	double.vk = vk
	_, hasNoList := compiledAllowedIPsFor(double, vk.ID, ctx)
	assert.False(t, hasNoList)
}

// fallbackStoreDouble is the minimal GovernanceStore the fallback branch serves: it resolves the
// presented key from its own field and delegates everything else to a real LocalGovernanceStore.
type fallbackStoreDouble struct {
	GovernanceStore
	vk *configstoreTables.TableVirtualKey
}

func (d *fallbackStoreDouble) GetVirtualKey(ctx context.Context, vkValue string) (*configstoreTables.TableVirtualKey, bool) {
	return d.vk, true
}

// TestParseAllowedIPsDropsInvalidEntry pins the config.json path: a list that skips the API's
// write-time validation must not widen the rule when one entry fails to compile — the bad entry
// is dropped and the compiled set only covers what parsed.
func TestParseAllowedIPsDropsInvalidEntry(t *testing.T) {
	nets := parseAllowedIPs([]string{"10.0.0.0/24", "not-an-ip", "10.0.0.0/33", "2001:db8::/32"})
	require.Len(t, nets, 2, "only the parseable entries compile")

	inside, err := parseAllowedIPEntry("10.0.0.0/24")
	require.NoError(t, err)
	assert.True(t, inside.Contains(netip(t, "10.0.0.5")))
	outside, err := parseAllowedIPEntry("2001:db8::/32")
	require.NoError(t, err)
	assert.True(t, outside.Contains(netip(t, "2001:db8::1")))
}

// netip parses a test address or fails the test: the compiled-set assertions compare parsed
// addresses, never strings.
func netip(t *testing.T, value string) net.IP {
	t.Helper()
	ip, ok := NormalizeClientIP(value)
	require.True(t, ok, "test address %q must parse", value)
	return ip
}
