package governance

import (
	"context"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// ipGateRecorder records every value a store pushes to the capture-gate hook.
type ipGateRecorder struct {
	mu     sync.Mutex
	values []bool
}

func (r *ipGateRecorder) record(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, v)
}

func (r *ipGateRecorder) last() (bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.values) == 0 {
		return false, false
	}
	return r.values[len(r.values)-1], true
}

func (r *ipGateRecorder) snapshot() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.values...)
}

// newIPGateStore builds an empty in-memory store with the capture-gate hook attached.
func newIPGateStore(t *testing.T) (*LocalGovernanceStore, *ipGateRecorder) {
	t.Helper()
	store, err := NewLocalGovernanceStore(t.Context(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)
	rec := &ipGateRecorder{}
	store.SetClientIPControlsHook(rec.record)
	return store, rec
}

// vkWithAllowedIPs builds a key whose only IP control is an allowed list.
func vkWithAllowedIPs(id string, entries []string) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKey(id, "sk-bf-"+id, id, true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai", []string{"*"})}
	vk.AllowedIPs = entries
	return vk
}

// vkWithIPLimit builds a key whose only IP control is a unique-IP rate limit.
func vkWithIPLimit(id string, maxIPs int64) *configstoreTables.TableVirtualKey {
	limitID := id + "-rl"
	return buildVirtualKeyWithRateLimit(id, "sk-bf-"+id, id, &configstoreTables.TableRateLimit{
		ID:              limitID,
		IPMaxLimit:      &maxIPs,
		IPResetDuration: ptrString("1h"),
	})
}

// ptrString is a local helper for the optional duration fields the builders take.
func ptrString(s string) *string { return &s }

// TestClientIPControlsFlagTracksVirtualKeyWritePaths pins B12 at the source the flag is
// computed from: a key carrying either IP control turns enforcement on, removing it turns
// enforcement off, and every in-memory write path that can change the answer publishes it.
func TestClientIPControlsFlagTracksVirtualKeyWritePaths(t *testing.T) {
	ctx := t.Context()
	t.Run("empty store reports no controls", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		require.False(t, store.HasClientIPControls())
		// Registration itself pushes the current answer, so an empty store has already
		// published one false before anything changed.
		require.Equal(t, []bool{false}, rec.snapshot())
	})

	t.Run("create with a list turns enforcement on", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithAllowedIPs("vk-a", []string{"10.0.0.1"}))
		require.True(t, store.HasClientIPControls())
		require.Equal(t, []bool{false, true}, rec.snapshot())
	})

	t.Run("create with a unique-ip limit turns enforcement on", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithIPLimit("vk-b", 2))
		require.True(t, store.HasClientIPControls())
		require.Equal(t, []bool{false, true}, rec.snapshot())
	})

	t.Run("create with token only limits leaves enforcement off", func(t *testing.T) {
		store, _ := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, buildVirtualKeyWithRateLimit("vk-c", "sk-bf-vk-c", "vk-c", buildRateLimit("vk-c-rl", 100, 10)))
		require.False(t, store.HasClientIPControls())
	})

	t.Run("explicit empty list counts as a configured rule", func(t *testing.T) {
		store, _ := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithAllowedIPs("vk-d", []string{}))
		require.True(t, store.HasClientIPControls(), "an empty list denies every IP unless explicitly allowed, so it needs the client IP")
	})

	t.Run("update that adds a list turns enforcement on", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithAllowedIPs("vk-e", nil))
		require.False(t, store.HasClientIPControls())
		store.UpdateVirtualKeyInMemory(ctx, vkWithAllowedIPs("vk-e", []string{"10.0.0.1"}), nil, nil, nil)
		require.True(t, store.HasClientIPControls())
		require.Equal(t, []bool{false, true}, rec.snapshot())
	})

	t.Run("update that clears both controls turns enforcement off", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithIPLimit("vk-f", 3))
		require.True(t, store.HasClientIPControls())
		store.UpdateVirtualKeyInMemory(ctx, vkWithAllowedIPs("vk-f", nil), nil, nil, nil)
		require.False(t, store.HasClientIPControls())
		last, _ := rec.last()
		require.False(t, last)
	})

	t.Run("delete turns enforcement off while another key keeps it on", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithAllowedIPs("vk-g", []string{"10.0.0.1"}))
		store.CreateVirtualKeyInMemory(ctx, vkWithAllowedIPs("vk-h", []string{"10.0.0.2"}))
		require.True(t, store.HasClientIPControls())
		store.DeleteVirtualKeyInMemory(ctx, "vk-g")
		require.True(t, store.HasClientIPControls(), "the remaining key still needs the client IP")
		store.DeleteVirtualKeyInMemory(ctx, "vk-h")
		require.False(t, store.HasClientIPControls())
		// The second create and the first delete do not change the answer, so they publish
		// nothing: only transitions reach the hook.
		require.Equal(t, []bool{false, true, false}, rec.snapshot())
	})
}

// TestClientIPControlsFlagTracksRateLimitWritePaths pins the rate-limit half of B12: adding,
// changing, or removing a unique-IP limit republishes the flag, and only a limit a virtual key
// actually holds counts (B14 — a team-held limit is never IP-checked).
func TestClientIPControlsFlagTracksRateLimitWritePaths(t *testing.T) {
	ctx := t.Context()
	maxIPs := int64(2)

	t.Run("upsert of an ip limit turns enforcement on", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		limitID := "vk-rl-rl"
		vk := vkWithAllowedIPs("vk-rl", nil)
		vk.RateLimitID = &limitID
		store.CreateVirtualKeyInMemory(ctx, vk)
		require.False(t, store.HasClientIPControls())
		store.UpsertRateLimitConfig(ctx, limitID, &configstoreTables.TableRateLimit{ID: limitID, IPMaxLimit: &maxIPs, IPResetDuration: ptrString("1h")})
		require.True(t, store.HasClientIPControls())
		require.Equal(t, []bool{false, true}, rec.snapshot())
	})

	t.Run("upsert that clears the ip limit turns enforcement off", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithIPLimit("vk-rl2", 2))
		require.True(t, store.HasClientIPControls())
		store.UpsertRateLimitConfig(ctx, "vk-rl2-rl", &configstoreTables.TableRateLimit{ID: "vk-rl2-rl"})
		require.False(t, store.HasClientIPControls())
		last, _ := rec.last()
		require.False(t, last)
	})

	t.Run("deleting the ip limit turns enforcement off", func(t *testing.T) {
		store, rec := newIPGateStore(t)
		store.CreateVirtualKeyInMemory(ctx, vkWithIPLimit("vk-rl3", 2))
		require.True(t, store.HasClientIPControls())
		store.DeleteRateLimit(ctx, "vk-rl3-rl")
		require.False(t, store.HasClientIPControls())
		last, _ := rec.last()
		require.False(t, last)
	})

	t.Run("a team-held ip limit is not a virtual key control", func(t *testing.T) {
		store, _ := newIPGateStore(t)
		teamLimitID := "team-rl"
		store.UpsertRateLimitConfig(ctx, teamLimitID, &configstoreTables.TableRateLimit{ID: teamLimitID, IPMaxLimit: &maxIPs, IPResetDuration: ptrString("1h")})
		require.False(t, store.HasClientIPControls(), "only a limit a virtual key carries is IP-checked (B14)")
	})
}

// TestClientIPControlsHookPushesSeedState pins the boot and config.json sync half of B12: the
// store is seeded before the hook exists (plugins are built after the config is loaded), so
// attaching the hook publishes the current answer rather than waiting for the next mutation.
func TestClientIPControlsHookPushesSeedState(t *testing.T) {
	seed := &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vkWithAllowedIPs("vk-seed", []string{"10.0.0.1"})},
	}
	store, err := NewLocalGovernanceStore(t.Context(), NewMockLogger(), nil, seed, nil, nil)
	require.NoError(t, err)
	require.True(t, store.HasClientIPControls(), "a key loaded from config.json must be visible to the flag")

	rec := &ipGateRecorder{}
	store.SetClientIPControlsHook(rec.record)
	require.Equal(t, []bool{true}, rec.snapshot())

	// A file-backed deployment with no IP controls pushes false, so a stale flag cannot
	// survive a restart onto a configuration without them.
	plain, err := NewLocalGovernanceStore(t.Context(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)
	plainRec := &ipGateRecorder{}
	plain.SetClientIPControlsHook(plainRec.record)
	require.Equal(t, []bool{false}, plainRec.snapshot())
}

// TestClientIPControlsInitWiresHook pins the plugin-instantiation path B12 names: Init and
// InitFromStore register the store's hook against the configured callback, so a transport that
// supplies one sees every later change without polling.
func TestClientIPControlsInitWiresHook(t *testing.T) {
	for _, tc := range []struct {
		name string
		init func(context.Context, *Config, *LocalGovernanceStore) (*GovernancePlugin, error)
	}{
		{
			name: "Init",
			init: func(ctx context.Context, cfg *Config, _ *LocalGovernanceStore) (*GovernancePlugin, error) {
				return Init(ctx, cfg, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil, nil, nil)
			},
		},
		{
			name: "InitFromStore",
			init: func(ctx context.Context, cfg *Config, store *LocalGovernanceStore) (*GovernancePlugin, error) {
				return InitFromStore(ctx, cfg, NewMockLogger(), store, nil, nil, nil, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &ipGateRecorder{}
			store, err := NewLocalGovernanceStore(t.Context(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil, nil)
			require.NoError(t, err)
			plugin, err := tc.init(t.Context(), &Config{ClientIPControlsChanged: rec.record}, store)
			require.NoError(t, err)
			require.NotNil(t, plugin)
			t.Cleanup(func() { _ = plugin.Cleanup() })

			first, ok := rec.last()
			require.True(t, ok, "instantiation must push the current state")
			require.False(t, first)

			plugin.GetGovernanceStore().CreateVirtualKeyInMemory(t.Context(), vkWithAllowedIPs("vk-init", []string{"10.0.0.1"}))
			last, _ := rec.last()
			require.True(t, last, "a later mutation must reach the transport callback")
		})
	}
}

// TestIPLimitDeltaClampsUnderflow pins the guard in vkIPLimitDelta: a loss without its matching
// gain is a caller bug, so the counter clamps at zero and resynchronizes from the keys rather
// than letting a negative count suppress the flag forever.
func TestIPLimitDeltaClampsUnderflow(t *testing.T) {
	store, _ := newIPGateStore(t)
	ctx := t.Context()
	store.CreateVirtualKeyInMemory(ctx, vkWithIPLimit("vk-clamp", 3))
	require.True(t, store.HasClientIPControls())

	// Two losses with one key carrying the control: the clamp resyncs from the entries and
	// keeps the flag on, because the key still does carry an IP limit.
	store.virtualKeyLostIPLimit()
	store.virtualKeyLostIPLimit()
	require.GreaterOrEqual(t, store.ipLimitedVKs.Load(), int64(0), "the counter cannot go negative")
	require.True(t, store.HasClientIPControls(), "the key's own entry is what the resync reads")
}
