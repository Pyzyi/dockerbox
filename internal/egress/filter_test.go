package egress

import (
	"net/netip"
	"testing"
)

func TestFilterDeniesNonPublic(t *testing.T) {
	f, _ := NewFilter(nil, nil, nil)
	denied := []string{
		"127.0.0.1", "10.1.2.3", "172.17.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "255.255.255.255", "224.0.0.1", "198.18.0.1",
		"::1", "fe80::1", "fd00::1", "::ffff:192.168.1.1", "::ffff:127.0.0.1",
		"64:ff9b::c0a8:101", "2002:c0a8:101::1", "2001:db8::1", "::",
		"::a00:1", "::ffff:0:a00:1",
	}
	for _, s := range denied {
		if f.Allowed(netip.MustParseAddr(s), 443) {
			t.Errorf("%s must be denied", s)
		}
	}
	allowed := []string{"1.1.1.1", "8.8.8.8", "140.82.112.3", "2606:4700:4700::1111", "::ffff:1.1.1.1"}
	for _, s := range allowed {
		if !f.Allowed(netip.MustParseAddr(s), 443) {
			t.Errorf("%s must be allowed", s)
		}
	}
}

func TestFilterAllowList(t *testing.T) {
	f, err := NewFilter([]string{"192.168.1.50", "10.0.0.0/24"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"192.168.1.50", "10.0.0.7"} {
		if !f.Allowed(netip.MustParseAddr(s), 443) {
			t.Errorf("%s should be allowed by allow-list", s)
		}
	}
	for _, s := range []string{"192.168.1.51", "10.0.1.1"} {
		if f.Allowed(netip.MustParseAddr(s), 443) {
			t.Errorf("%s must stay denied", s)
		}
	}
	if _, err := NewFilter([]string{"not-a-cidr"}, nil, nil); err == nil {
		t.Error("invalid entry accepted")
	}
}

func TestFilterDenyListWins(t *testing.T) {
	// e.g. the host's own public IP and a LAN range that is also allow-listed
	f, err := NewFilter([]string{"192.168.1.0/24"}, []string{"203.0.114.7", "192.168.1.1/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"203.0.114.7", "192.168.1.1"} {
		if f.Allowed(netip.MustParseAddr(s), 443) {
			t.Errorf("%s must be denied", s)
		}
	}
	if !f.Allowed(netip.MustParseAddr("192.168.1.2"), 443) || !f.Allowed(netip.MustParseAddr("203.0.114.8"), 443) {
		t.Error("deny list too broad")
	}
}

func TestFilterPorts(t *testing.T) {
	// One LAN service on one port, public traffic limited to web ports.
	f, err := NewFilter([]string{"192.168.1.50:11434", "[fd00::5]:8080"}, nil, []int{80, 443})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ip   string
		port int
		want bool
	}{
		{"192.168.1.50", 11434, true},
		{"192.168.1.50", 22, false}, // other services on the allowed host stay closed
		{"192.168.1.50", 443, false},
		{"fd00::5", 8080, true},
		{"1.1.1.1", 443, true},
		{"1.1.1.1", 25, false}, // no SMTP from boxes when ports are restricted
		{"1.1.1.1", 0, false},
	}
	for _, c := range cases {
		if got := f.Allowed(netip.MustParseAddr(c.ip), c.port); got != c.want {
			t.Errorf("%s:%d = %v, want %v", c.ip, c.port, got, c.want)
		}
	}
	if _, err := NewFilter(nil, nil, []int{70000}); err == nil {
		t.Error("invalid port accepted")
	}
}
