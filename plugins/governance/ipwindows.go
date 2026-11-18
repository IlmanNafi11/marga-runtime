package governance

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
)

// ipWindow is one rate limit's rolling set of client IPs that were admitted inside the
// current window. Recording happens at admission and nowhere else: a refused IP is never
// inserted, so the set stops growing at max and its memory is bounded by the limit an
// operator configured, not by traffic.
//
// All mutation happens under mu, which is what makes admission one atomic step: two
// requests from different IPs arriving together cannot both read a set that is one short
// of full and both insert into it.
type ipWindow struct {
	mu          sync.Mutex
	ips         map[string]struct{} // normalized IPs; refused IPs are never inserted
	start       time.Time           // when the current window opened
	max         int64
	duration    time.Duration
	durationRaw string // the configured spelling, compared to detect a duration change
}

// ipWindows holds every rate limit's window, keyed by the rate-limit row ID. The map is
// written only on config mutation and read once per request that carries an IP limit; a
// key without one has no entry at all, which is the common case (B13).
type ipWindows struct {
	m sync.Map // rate-limit ID -> *ipWindow
}

// load returns the window for a rate limit, or nil when none was opened yet.
func (w *ipWindows) load(id string) *ipWindow {
	raw, ok := w.m.Load(id)
	if !ok {
		return nil
	}
	rec, _ := raw.(*ipWindow)
	return rec
}

// drop removes one rate limit's window.
func (w *ipWindows) drop(id string) {
	w.m.Delete(id)
}

// clear removes every window, for a full rebuild.
func (w *ipWindows) clear() {
	w.m.Range(func(key, _ any) bool {
		w.m.Delete(key)
		return true
	})
}

// admit checks whether ip may open a new slot under max and records it when it may, as
// one step under the window's mutex. A repeat of an IP already in the set is admitted
// without consuming another slot. count is the number of distinct IPs in the window at
// the moment of the decision.
//
// The record self-heals from any publish path: a changed duration re-anchors with an
// empty set (B11) and a changed max is adopted on the spot, so a rate limit written by a
// path that never called reconcile still enforces the configuration the check just read.
func (w *ipWindows) admit(id string, ip net.IP, max int64, rawDuration string, duration time.Duration) (count int64, admitted bool) {
	now := time.Now()
	raw, loaded := w.m.LoadOrStore(id, &ipWindow{
		ips:         map[string]struct{}{},
		start:       now,
		max:         max,
		duration:    duration,
		durationRaw: rawDuration,
	})
	rec, _ := raw.(*ipWindow)
	if rec == nil {
		// A foreign value under this key cannot happen in this package; refuse rather
		// than panic on it.
		return 0, false
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if loaded {
		if rec.durationRaw != rawDuration {
			rec.ips = map[string]struct{}{}
			rec.start = now
			rec.durationRaw = rawDuration
			rec.duration = duration
		}
		rec.max = max
	}
	// Lazy expiry: no ticker, no background sweep. The first access after the window
	// closed sees it closed and opens a fresh one.
	if now.Sub(rec.start) >= rec.duration {
		rec.ips = map[string]struct{}{}
		rec.start = now
	}

	key := ip.String()
	if _, seen := rec.ips[key]; seen {
		return int64(len(rec.ips)), true
	}
	if int64(len(rec.ips)) >= rec.max {
		return int64(len(rec.ips)), false
	}
	rec.ips[key] = struct{}{}
	return int64(len(rec.ips)), true
}

// reconcile applies an upserted rate limit's IP configuration to its open window (B11):
// a changed duration re-anchors with an empty set, a raised or unchanged max keeps the
// set, and a cleared max drops the record.
func (w *ipWindows) reconcile(id string, config *configstoreTables.TableRateLimit) {
	if config == nil || config.IPMaxLimit == nil {
		w.drop(id)
		return
	}
	rec := w.load(id)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if config.IPResetDuration != nil && rec.durationRaw != *config.IPResetDuration {
		duration, err := configstoreTables.ParseDuration(*config.IPResetDuration)
		if err == nil {
			rec.ips = map[string]struct{}{}
			rec.start = time.Now()
			rec.durationRaw = *config.IPResetDuration
			rec.duration = duration
		}
	}
	rec.max = *config.IPMaxLimit
}

// ipWindowViolation reports whether the request's client IP exceeds the rate limit's
// unique-IP window. The limit must be held by a virtual key (B14) and carry a positive
// max with a parseable duration; anything else is not this check's to enforce. An
// unresolvable IP fails open (B9): membership cannot be proven, and the allowed-IP list
// is the gate that fails closed.
func (gs *LocalGovernanceStore) ipWindowViolation(ctx context.Context, limit schemas.Limit, rateLimit *configstoreTables.TableRateLimit) (string, bool) {
	if rateLimit == nil || rateLimit.IPMaxLimit == nil || rateLimit.IPResetDuration == nil {
		return "", false
	}
	if limit.HolderKind != string(grant.LimitHolderVirtualKey) {
		return "", false
	}
	raw, _ := ctx.Value(schemas.BifrostContextKeyClientIP).(string)
	ip, resolvable := NormalizeClientIP(raw)
	if !resolvable {
		return "", false
	}
	duration, err := configstoreTables.ParseDuration(*rateLimit.IPResetDuration)
	if err != nil || duration <= 0 {
		return "", false
	}
	count, admitted := gs.ipWindows.admit(rateLimit.ID, ip, *rateLimit.IPMaxLimit, *rateLimit.IPResetDuration, duration)
	if admitted {
		return "", false
	}
	return fmt.Sprintf("ip limit exceeded (%d/%d distinct client IPs, resets every %s)", count, *rateLimit.IPMaxLimit, *rateLimit.IPResetDuration), true
}

// rewind ages a rate limit's window past its duration without sleeping, for tests of the
// lazy-expiry roll. A window with no record yet is a no-op.
func (w *ipWindows) rewind(id string, past time.Duration) {
	rec := w.load(id)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.start = rec.start.Add(-past)
	rec.mu.Unlock()
}

// record returns a rate limit's window for inspection, or nil when none is open. Test and
// lifecycle assertions read it; the request path never does.
func (w *ipWindows) record(id string) *ipWindow {
	return w.load(id)
}
