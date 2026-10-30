package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
)

// DNSServer answers every A query with the proxy's own address on the network
// the query came from, so programs in a box that ignore HTTP(S)_PROXY still
// end up at the proxy (see ServeTransparent). It never resolves anything
// itself: other record types get an empty answer and boxes learn no real
// addresses.
type DNSServer struct {
	// LocalAddr returns the proxy's address facing client; nil uses the
	// network interfaces.
	LocalAddr func(client netip.Addr) (netip.Addr, bool)
}

const (
	dnsTypeA   = 1
	dnsClassIN = 1
	dnsTTL     = 30
)

// Serve answers queries on pc until ctx ends.
func (s *DNSServer) Serve(ctx context.Context, pc net.PacketConn) error {
	go func() { <-ctx.Done(); pc.Close() }()
	if s.LocalAddr == nil {
		s.LocalAddr = interfaceAddrFacing
	}
	buf := make([]byte, 512)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			continue
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		client, _ := netip.AddrFromSlice(ua.IP)
		self, ok := s.LocalAddr(client.Unmap())
		if !ok {
			continue
		}
		if resp := dnsAnswer(buf[:n], self); resp != nil {
			_, _ = pc.WriteTo(resp, from)
		}
	}
}

// dnsAnswer builds the response to query q, or nil if q is not a query.
func dnsAnswer(q []byte, self netip.Addr) []byte {
	if len(q) < 12 || q[2]&0x80 != 0 { // too short or already a response
		return nil
	}
	opcode := (q[2] >> 3) & 0x0f
	qd := binary.BigEndian.Uint16(q[4:6])
	resp := make([]byte, 12, 64)
	copy(resp, q[:4])
	resp[2] = 0x80 | q[2]&0x79 // QR, keep opcode and RD
	resp[3] = 0x80             // RA, rcode 0
	if opcode != 0 || qd != 1 {
		resp[3] |= 4 // NOTIMP
		return resp
	}
	// question: name labels, type, class
	i := 12
	for {
		if i >= len(q) {
			return nil
		}
		l := int(q[i])
		if l == 0 {
			i++
			break
		}
		if l&0xc0 != 0 || i+1+l > len(q) {
			return nil
		}
		i += 1 + l
	}
	if i+4 > len(q) || i-12 > 255 {
		return nil
	}
	qtype := binary.BigEndian.Uint16(q[i : i+2])
	qclass := binary.BigEndian.Uint16(q[i+2 : i+4])
	resp = append(resp, q[12:i+4]...)
	binary.BigEndian.PutUint16(resp[4:6], 1)
	if qtype == dnsTypeA && qclass == dnsClassIN && self.Is4() {
		binary.BigEndian.PutUint16(resp[6:8], 1)
		ip := self.As4()
		resp = append(resp, 0xc0, 12, 0, dnsTypeA, 0, dnsClassIN, 0, 0, 0, dnsTTL, 0, 4)
		resp = append(resp, ip[:]...)
	}
	return resp
}

// interfaceAddrFacing returns the local IPv4 address in the same subnet as client.
func interfaceAddrFacing(client netip.Addr) (netip.Addr, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return netip.Addr{}, false
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		local, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		local = local.Unmap()
		ones, _ := ipn.Mask.Size()
		if pfx, err := local.Prefix(ones); err == nil && local.Is4() && pfx.Contains(client) && !local.IsLoopback() {
			return local, true
		}
	}
	return netip.Addr{}, false
}
