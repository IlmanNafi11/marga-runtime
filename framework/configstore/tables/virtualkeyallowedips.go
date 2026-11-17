package tables

import (
	"fmt"
	"net"
	"strings"
)

// ValidateAllowedIPs checks a virtual key's allowed_ips list. Every entry must be a single IPv4 or
// IPv6 address, or a CIDR block of either, as written: blank entries and entries with surrounding
// whitespace are rejected rather than trimmed, because storage keeps exactly what the operator
// wrote and enforcement is where normalization happens (trim, port, zone, v4-mapped form). Rejecting
// here is what keeps a padded entry from silently matching nothing at request time.
func ValidateAllowedIPs(ips []string) error {
	for _, entry := range ips {
		if entry == "" || strings.TrimSpace(entry) == "" {
			return fmt.Errorf("allowed_ips entry cannot be empty")
		}
		if strings.ContainsRune(entry, '/') {
			if _, _, err := net.ParseCIDR(entry); err != nil {
				return fmt.Errorf("allowed_ips entry %q is not a valid CIDR block", entry)
			}
			continue
		}
		if net.ParseIP(entry) == nil {
			return fmt.Errorf("allowed_ips entry %q is not a valid IP address", entry)
		}
	}
	return nil
}
