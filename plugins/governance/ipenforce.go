package governance

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

// NormalizeClientIP resolves one client-IP spelling to a comparable address: surrounding
// whitespace, a port suffix, IPv6 brackets and a zone are stripped, then the rest must parse as
// a single IPv4 or IPv6 address. The IPv4-mapped form (::ffff:a.b.c.d) is folded to its IPv4
// value so it matches that address's IPv4 entries and CIDRs.
//
// ok is false for anything that does not resolve to exactly one address — an empty value,
// garbage, a bare hostname, a list ("a,b"). Callers treat that as unresolvable and apply the
// fail rule their configuration implies (B9: closed under an allowlist, open under the limit).
//
// Normalization happens here and nowhere else: storage keeps the operator's entries verbatim,
// and no comparison in this package ever compares two strings.
func NormalizeClientIP(value string) (net.IP, bool) {
	s := strings.TrimSpace(value)
	if s == "" {
		return nil, false
	}
	// A bracketed IPv6 literal reads through the brackets; anything after "]" is the port.
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return nil, false
		}
		s = s[1:end]
	} else if strings.Count(s, ":") == 1 {
		// Exactly one colon can only be a v4 address with a port (a v6 literal has more).
		// A port is stripped only when what precedes it is itself an address, so a value
		// that is not one stays unparseable rather than silently losing its last octet.
		if host, _, found := strings.Cut(s, ":"); found && net.ParseIP(host) != nil {
			s = host
		}
	}
	// The zone index (%eth0) names an interface on the reporting host and never reaches a
	// cross-host policy decision.
	if i := strings.IndexByte(s, '%'); i >= 0 {
		s = s[:i]
	}
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return nil, false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4, true
	}
	return ip, true
}

// parseAllowedIPEntry compiles one stored allowed_ips entry into the network it names. A bare
// address compiles to its /32 (v4) or /128 (v6) network; a CIDR block compiles to that network
// with host bits zeroed, so an entry stored with host bits set (ParseCIDR tolerates them)
// enforces as the network it names rather than as the spelling it was written with.
//
// A parse failure returns nil: a stored list is validated at write time, but a list loaded from
// a config.json the validation never saw must not widen the rule, so a failed entry is dropped
// from the compiled set and the gate keeps refusing what the remainder does not cover.
func parseAllowedIPEntry(entry string) (*net.IPNet, error) {
	entry = strings.TrimSpace(entry)
	if strings.Contains(entry, "/") {
		_, ipNet, err := net.ParseCIDR(entry)
		return ipNet, err
	}
	ip := net.ParseIP(entry)
	if ip == nil {
		return nil, fmt.Errorf("allowed_ips entry %q is not an IP address", entry)
	}
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

// parseAllowedIPs compiles a whole stored list. Entries that fail to compile are dropped: the
// gate refuses anything the compiled set does not cover, so a dropped entry narrows the rule
// rather than widening it. A nil entry list compiles to nil, which is how the gate tells "no
// list configured" apart from "a list that admits nothing" (an explicitly empty, non-nil list).
func parseAllowedIPs(entries []string) []*net.IPNet {
	if entries == nil {
		return nil
	}
	nets := make([]*net.IPNet, 0, len(entries))
	for _, entry := range entries {
		if ipNet, err := parseAllowedIPEntry(entry); err == nil && ipNet != nil {
			nets = append(nets, ipNet)
		}
	}
	return nets
}

// ipAllowedInList reports whether ip is covered by a compiled allowlist. An empty list admits
// nothing: that is the deliberate "deny every client IP" configuration, a different answer from
// nil, which means the rule is off.
func ipAllowedInList(nets []*net.IPNet, ip net.IP) bool {
	for _, ipNet := range nets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// allowedIPList is one key's compiled allowlist, compiled once at load and replaced as a whole
// on every config publish. Readers take the pointer and walk it; a concurrent republish swaps it
// and never mutates the slice a request is already reading.
type allowedIPList struct {
	nets []*net.IPNet
}

// allowedIPsCache holds each virtual key's compiled allowlist, keyed by VK row ID. Keying by ID
// rather than by the row object keeps it correct through the grace-period aliases and through a
// value rotation (both point at the same row), and it survives the row being re-cloned on update
// as long as the ID does. Entries are dropped wherever a key is deleted or replaced; a deleted
// key that somehow lingers only costs its own bounded slice, never a wrong decision.
//
// The map is written only on config mutation, so a request costs one sync.Map load. A key with
// no list has no entry at all, which is the common case and the reason the gate reads this map
// instead of the row: absence is the answer, and it costs one load with no row walk (B13).
type allowedIPsCache struct {
	byVKID sync.Map // string (VK row ID) -> *allowedIPList
	// present counts the keys carrying a list, so the capture gate can ask whether anything
	// needs the client IP at all without walking the map.
	present atomic.Int64
}

// compileAllowedIPs compiles entries into a cache value. Nil entries compile to nil, meaning the
// rule is off for the key.
func compileAllowedIPs(entries []string) *allowedIPList {
	if entries == nil {
		return nil
	}
	return &allowedIPList{nets: parseAllowedIPs(entries)}
}

// load returns the key's compiled list. The second result reports whether the key carries a list
// at all (including an explicit empty one), which is what the gate branches on.
func (c *allowedIPsCache) load(vkID string) (*allowedIPList, bool) {
	if vkID == "" {
		return nil, false
	}
	value, ok := c.byVKID.Load(vkID)
	if !ok || value == nil {
		return nil, false
	}
	list, ok := value.(*allowedIPList)
	if !ok || list == nil {
		return nil, false
	}
	return list, true
}

// store publishes a key's compiled list, or drops the entry when the key carries none.
func (c *allowedIPsCache) store(vkID string, list *allowedIPList) {
	if vkID == "" {
		return
	}
	if list == nil {
		c.delete(vkID)
		return
	}
	_, existed := c.byVKID.Swap(vkID, list)
	if !existed {
		c.present.Add(1)
	}
}

// storeEntries compiles and publishes a key's entries; nil entries drop the entry.
func (c *allowedIPsCache) storeEntries(vkID string, entries []string) {
	c.store(vkID, compileAllowedIPs(entries))
}

// delete drops one key's compiled list.
func (c *allowedIPsCache) delete(vkID string) {
	if vkID == "" {
		return
	}
	if _, existed := c.byVKID.LoadAndDelete(vkID); existed {
		c.present.Add(-1)
	}
}

// any reports whether any key carries a list. It is the capture gate's question: with no list
// anywhere and no IP limit configured, the client IP need not be resolved at all.
func (c *allowedIPsCache) any() bool {
	return c.present.Load() > 0
}

// clear drops every compiled list, for a full rebuild.
func (c *allowedIPsCache) clear() {
	c.byVKID = sync.Map{}
	c.present.Store(0)
}
