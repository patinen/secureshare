package clientip

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	for _, c := range []struct{ name, peer, trust, forward, want string }{
		{"direct_v4", "192.0.2.1:123", "", "", "192.0.2.1"},
		{"direct_v6", "[2001:db8::1]:123", "", "", "2001:db8::1"},
		{"mapped_v4", "[::ffff:192.0.2.1]:123", "", "", "192.0.2.1"},
		{"spoof_ignored", "192.0.2.1:123", "10.0.0.0/8", "198.51.100.9", "192.0.2.1"},
		{"trusted_single", "10.0.0.1:123", "10.0.0.0/8", "198.51.100.9", "198.51.100.9"},
		{"trusted_multihop", "10.0.0.1:123", "10.0.0.0/8", "198.51.100.9, 10.0.0.2", "198.51.100.9"},
		{"stop_untrusted_hop", "10.0.0.1:123", "10.0.0.0/8", "203.0.113.7, 198.51.100.9", "198.51.100.9"},
		{"v6_proxy", "[fd00::1]:123", "fd00::/8", "2001:db8::2, fd00::2", "2001:db8::2"},
		{"no_header", "10.0.0.1:123", "10.0.0.0/8", "", "10.0.0.1"},
		{"malformed_chain", "10.0.0.1:123", "10.0.0.0/8", "bad, 198.51.100.9", "10.0.0.1"},
		{"empty_entry", "10.0.0.1:123", "10.0.0.0/8", ",198.51.100.9", "10.0.0.1"},
		{"port_rejected", "10.0.0.1:123", "10.0.0.0/8", "198.51.100.9:12", "10.0.0.1"},
		{"zone_rejected", "10.0.0.1:123", "10.0.0.0/8", "fe80::1%eth0", "10.0.0.1"},
		{"long_header", "10.0.0.1:123", "10.0.0.0/8", strings.Repeat("x", 4097), "10.0.0.1"},
		{"too_many_hops", "10.0.0.1:123", "10.0.0.0/8", strings.Repeat("10.0.0.2,", 32) + "198.51.100.9", "10.0.0.1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resolver, err := Parse(c.trust)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.peer
			r.Header.Set("X-Forwarded-For", c.forward)
			r.Header.Set("X-Real-IP", "203.0.113.1")
			r.Header.Set("CF-Connecting-IP", "203.0.113.1")
			r.Header.Set("Forwarded", "for=203.0.113.1")
			got, err := resolver.Resolve(r)
			if err != nil || got.String() != c.want {
				t.Fatal("unsafe client IP resolution")
			}
		})
	}
	t.Run("invalid_cidr", func(t *testing.T) {
		if _, err := Parse("localhost"); err == nil {
			t.Fatal("invalid trust accepted")
		}
	})
	t.Run("invalid_peer", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "invalid"
		if _, err := (Resolver{}).Resolve(r); err == nil {
			t.Fatal("unknown peer accepted")
		}
	})
}
