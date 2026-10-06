package clientip

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type Resolver struct{ Trusted []netip.Prefix }

func Parse(raw string) (Resolver, error) {
	r := Resolver{}
	if strings.TrimSpace(raw) == "" {
		return r, nil
	}
	for _, part := range strings.Split(raw, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return Resolver{}, errors.New("invalid trusted proxy CIDR")
		}
		r.Trusted = append(r.Trusted, p.Masked())
	}
	return r, nil
}
func (r Resolver) trusted(ip netip.Addr) bool {
	for _, p := range r.Trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
func (r Resolver) Resolve(req *http.Request) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || peer.Zone() != "" {
		return netip.Addr{}, errors.New("invalid network peer")
	}
	peer = peer.Unmap()
	if !r.trusted(peer) {
		return peer, nil
	}
	raw := strings.Join(req.Header.Values("X-Forwarded-For"), ",")
	if raw == "" || len(raw) > 4096 {
		return peer, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 32 {
		return peer, nil
	}
	chain := make([]netip.Addr, len(parts))
	for i, part := range parts {
		ip, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil || ip.Zone() != "" {
			return peer, nil
		}
		chain[i] = ip.Unmap()
	}
	// Stop at the first untrusted hop walking right to left. Anything to its
	// left could have been supplied by that untrusted client. If all hops are
	// trusted, the leftmost valid address is the client.
	for i := len(chain) - 1; i >= 0; i-- {
		if !r.trusted(peer) {
			break
		}
		peer = chain[i]
	}
	return peer, nil
}
