package samba

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// Addr is an address of an interface with its prefix, and whether it is in a private range: IPv4 10/8, 172.16/12 and
// 192.168/16, IPv6 fc00::/7 and fe80::/10.
type Addr struct {
	Iface, CIDR string
	Private     bool
}

// Iface is a network interface. Bridge is set for the Docker and libvirt names docker0, br-*, virbr* and veth*.
type Iface struct {
	Name   string
	Addrs  []Addr
	Bridge bool
}

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
}

func private(a netip.Addr) bool {
	for _, p := range privateRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func isBridge(name string) bool {
	return name == "docker0" || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") || strings.HasPrefix(name, "veth")
}

// SystemInterfaces lists the interfaces of the host that the kernel reports (net.Interfaces and Addrs: the addresses
// of each interface with its prefix, whatever the routing tables hold), sorted by name, without the loopback interface.
// Addresses are IPv4 first, then IPv6, each in address order.
func SystemInterfaces() ([]Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Iface
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			return nil, err
		}
		out = append(out, ifaceOf(i.Name, addrs))
	}
	slices.SortFunc(out, func(a, b Iface) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ifaceOf builds an Iface from the addresses that net reports; anything that is not an address with a prefix is ignored.
func ifaceOf(name string, addrs []net.Addr) Iface {
	type pa struct {
		addr   netip.Addr
		prefix netip.Prefix
	}
	var list []pa
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		ones, bits := n.Mask.Size()
		if ip.Is4() && bits == 128 { // an IPv4 address on a 16 byte IP with a 16 byte mask
			ones -= 96
		}
		if ones < 0 || ones > ip.BitLen() {
			continue
		}
		list = append(list, pa{ip, netip.PrefixFrom(ip, ones)})
	}
	slices.SortFunc(list, func(x, y pa) int {
		if x.addr.Is4() != y.addr.Is4() {
			if x.addr.Is4() {
				return -1
			}
			return 1
		}
		return x.addr.Compare(y.addr)
	})
	i := Iface{Name: name, Bridge: isBridge(name)}
	for _, a := range list {
		i.Addrs = append(i.Addrs, Addr{Iface: name, CIDR: a.prefix.String(), Private: private(a.addr)})
	}
	return i
}

// BridgeSubnets are the networks of the addresses of the bridges among ifs (Iface.Bridge) whose names are not chosen,
// each once, sorted (IPv4 first). Linux takes a packet for any local address on whatever interface it arrives, so a
// container or a VM on a bridge reaches the address of a chosen interface too: hosts allow excepts these networks, and
// check-share refuses a client in one. A link-local network is not among them: every interface has fe80::/64, and the
// kernel takes a packet for a link-local address only on its own link.
func BridgeSubnets(ifs []Iface, chosen []string) []netip.Prefix {
	var out []netip.Prefix
	for _, i := range ifs {
		if !i.Bridge || slices.Contains(chosen, i.Name) {
			continue
		}
		for _, a := range i.Addrs {
			p, err := netip.ParsePrefix(a.CIDR)
			if err != nil || p.Addr().IsLinkLocalUnicast() {
				continue
			}
			out = append(out, p.Masked())
		}
	}
	slices.SortFunc(out, func(x, y netip.Prefix) int {
		if c := x.Addr().Compare(y.Addr()); c != 0 {
			return c
		}
		return x.Bits() - y.Bits()
	})
	return slices.Compact(out)
}

// DefaultInterfaces are the interfaces to offer, and to store once at setup: not lo, not a bridge, and every address of the
// interface (at least one) in a private range. An interface that also has a public address stays out.
func DefaultInterfaces(ifs []Iface) []string {
	var out []string
	for _, i := range ifs {
		if i.Name == "lo" || i.Bridge || len(i.Addrs) == 0 {
			continue
		}
		if !slices.ContainsFunc(i.Addrs, func(a Addr) bool { return !a.Private }) {
			out = append(out, i.Name)
		}
	}
	return out
}
