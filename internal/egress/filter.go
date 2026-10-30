// Package egress implements the forward proxy that is the only way out of an
// agent box (ADR 0003). It denies every non-public destination.
package egress

import (
	"fmt"
	"net/netip"
)

// deniedPrefixes complements the netip.Addr predicates with special-purpose
// ranges that are not "private" in the stdlib sense but must not be reachable.
var deniedPrefixes = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // carrier-grade NAT, also used by Tailscale
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"240.0.0.0/4",     // reserved, includes 255.255.255.255
	"64:ff9b::/96",    // NAT64 can embed private IPv4 addresses
	"64:ff9b:1::/48",  // local-use NAT64
	"2002::/16",       // 6to4 can embed private IPv4 addresses
	"2001::/32",       // Teredo
	"2001:db8::/32",   // documentation
	"100::/64",        // discard-only
	"::/96",           // IPv4-compatible (deprecated) can embed private IPv4
	"::ffff:0:0:0/96", // SIIT IPv4-translated
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// Filter decides whether a destination address and port may be contacted.
type Filter struct {
	allow []allowRule
	deny  []netip.Prefix
	ports map[int]bool // empty: every port
}

// allowRule is an explicit exception; port 0 means every port.
type allowRule struct {
	prefix netip.Prefix
	port   int
}

// NewFilter returns a filter.
//   - allow: explicit exceptions for private targets, e.g. a local LLM server.
//     Entries are "ADDR:PORT" / "[V6ADDR]:PORT" (recommended: one service) or
//     an address/CIDR (every port).
//   - deny: additional public addresses, e.g. the host's own public/WAN IP.
//     Deny wins over allow.
//   - ports: if non-empty, the only destination ports allowed for targets that
//     are not covered by a port-specific allow entry.
func NewFilter(allow, deny []string, ports []int) (*Filter, error) {
	f := &Filter{ports: map[int]bool{}}
	for _, a := range allow {
		if ap, err := netip.ParseAddrPort(a); err == nil {
			f.allow = append(f.allow, allowRule{netip.PrefixFrom(ap.Addr().Unmap(), ap.Addr().Unmap().BitLen()), int(ap.Port())})
			continue
		}
		p, err := parsePrefixes([]string{a})
		if err != nil {
			return nil, err
		}
		f.allow = append(f.allow, allowRule{prefix: p[0]})
	}
	var err error
	if f.deny, err = parsePrefixes(deny); err != nil {
		return nil, err
	}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %d", p)
		}
		f.ports[p] = true
	}
	return f, nil
}

func parsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			addr, aerr := netip.ParseAddr(c)
			if aerr != nil {
				return nil, fmt.Errorf("invalid CIDR %q: %w", c, err)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Allowed reports whether ip:port may be contacted.
func (f *Filter) Allowed(ip netip.Addr, port int) bool {
	ip = ip.Unmap() // ::ffff:10.0.0.1 is 10.0.0.1
	if !ip.IsValid() || port < 1 || port > 65535 {
		return false
	}
	for _, p := range f.deny {
		if p.Contains(ip) {
			return false
		}
	}
	portOK := len(f.ports) == 0 || f.ports[port]
	for _, r := range f.allow {
		if r.prefix.Contains(ip) {
			if r.port != 0 {
				if r.port == port {
					return true
				}
				continue
			}
			return portOK
		}
	}
	return portOK && IsPublic(ip)
}

// IsPublic reports whether ip is a globally routable unicast address.
func IsPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range deniedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
