package tables

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestRateLimitBeforeSaveIPFields pins the BeforeSave validation of the client-IP limit pair,
// mirroring the token/request rules exactly: a max without a duration is rejected, a non-positive
// max is rejected, and a duration that does not parse or is not positive is rejected. A duration
// without a max is accepted, exactly as it is for token and request limits.
func TestRateLimitBeforeSaveIPFields(t *testing.T) {
	tests := []struct {
		name    string
		max     *int64
		reset   *string
		wantErr string
	}{
		{name: "both nil is the unset feature", max: nil, reset: nil},
		{name: "valid pair", max: new(int64(5)), reset: new("1h")},
		{name: "duration without max mirrors token/request", max: nil, reset: new("1h")},
		{name: "max without duration", max: new(int64(5)), reset: nil, wantErr: "ip_reset_duration is required when ip_max_limit is set"},
		{name: "zero max", max: new(int64(0)), reset: new("1h"), wantErr: "ip_max_limit cannot be zero or negative: 0"},
		{name: "negative max", max: new(int64(-1)), reset: new("1h"), wantErr: "ip_max_limit cannot be zero or negative: -1"},
		{name: "unparsable duration", max: new(int64(5)), reset: new("soon"), wantErr: "invalid ip reset duration format: soon"},
		{name: "zero duration", max: new(int64(5)), reset: new("0s"), wantErr: "ip reset duration cannot be zero or negative: 0s"},
		{name: "negative duration", max: new(int64(5)), reset: new("-5m"), wantErr: "ip reset duration cannot be zero or negative: -5m"},
		{name: "duration checked even without max", max: nil, reset: new("nope"), wantErr: "invalid ip reset duration format: nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := &TableRateLimit{IPMaxLimit: tt.max, IPResetDuration: tt.reset}
			err := rl.BeforeSave(nil)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestValidateAllowedIPs covers the accept/reject set from the spec: single IPv4 and IPv6
// addresses, v4 and v6 CIDRs, and the rejects — an out-of-range CIDR, an invalid address, an
// empty string, and untrimmed whitespace. Normalization is an enforcement-time concern; storage
// keeps what the operator wrote, so a whitespace-padded entry is rejected rather than trimmed.
func TestValidateAllowedIPs(t *testing.T) {
	tests := []struct {
		name    string
		ips     []string
		wantErr string
	}{
		{name: "nil leaves the feature off", ips: nil},
		{name: "empty list", ips: []string{}},
		{name: "single ipv4", ips: []string{"10.0.0.1"}},
		{name: "single ipv6", ips: []string{"2001:db8::1"}},
		{name: "ipv4 cidr", ips: []string{"10.0.0.0/24"}},
		{name: "ipv6 cidr", ips: []string{"2001:db8::/32"}},
		{name: "mixed", ips: []string{"10.0.0.1", "2001:db8::1", "10.0.0.0/24", "2001:db8::/32"}},
		{name: "ipv4 out of range cidr", ips: []string{"10.0.0.0/33"}, wantErr: "10.0.0.0/33"},
		{name: "ipv6 out of range cidr", ips: []string{"2001:db8::/200"}, wantErr: "2001:db8::/200"},
		{name: "invalid address", ips: []string{"999.1.1.1"}, wantErr: "999.1.1.1"},
		{name: "empty entry", ips: []string{""}, wantErr: "empty"},
		{name: "whitespace-only entry", ips: []string{"   "}, wantErr: "empty"},
		{name: "untrimmed whitespace is rejected, not trimmed", ips: []string{" 10.0.0.1"}, wantErr: " 10.0.0.1"},
		{name: "trailing whitespace is rejected, not trimmed", ips: []string{"10.0.0.1 "}, wantErr: "10.0.0.1 "},
		{name: "one bad entry names it", ips: []string{"10.0.0.1", "nonsense"}, wantErr: "nonsense"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAllowedIPs(tt.ips)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestAllowedIPsRoundTrip pins the storage shape: a nil list stays nil (feature off), an explicit
// empty list survives as an empty list, and a populated list survives a write/read cycle through
// the JSON serializer.
func TestAllowedIPsRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TableVirtualKey{}))

	t.Run("absent list stays nil", func(t *testing.T) {
		vk := TableVirtualKey{ID: "ip-nil", Name: "ip-nil", Value: *schemas.NewSecretVar("sk-bf-ip-nil")}
		require.NoError(t, db.Create(&vk).Error)
		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		assert.Nil(t, stored.AllowedIPs)
	})

	t.Run("empty list stays empty", func(t *testing.T) {
		vk := TableVirtualKey{ID: "ip-empty", Name: "ip-empty", Value: *schemas.NewSecretVar("sk-bf-ip-empty"), AllowedIPs: []string{}}
		require.NoError(t, db.Create(&vk).Error)

		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		require.NotNil(t, stored.AllowedIPs, "[] must not come back as nil, which would silently turn the rule off")
		assert.Empty(t, stored.AllowedIPs)
	})

	t.Run("populated list survives the round trip", func(t *testing.T) {
		vk := TableVirtualKey{
			ID: "ip-list", Name: "ip-list", Value: *schemas.NewSecretVar("sk-bf-ip-list"),
			AllowedIPs: []string{"10.0.0.1", "2001:db8::1", "10.0.0.0/24"},
		}
		require.NoError(t, db.Create(&vk).Error)

		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		assert.Equal(t, []string{"10.0.0.1", "2001:db8::1", "10.0.0.0/24"}, stored.AllowedIPs)
	})
}
