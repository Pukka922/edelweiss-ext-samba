package samba

import (
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func ipnet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip // as net reports an interface address: the address, not the network
	return n
}

func TestIfaceOf(t *testing.T) {
	v4in6 := &net.IPNet{IP: net.ParseIP("10.8.0.2").To16(), Mask: net.CIDRMask(32, 32)}
	got := ifaceOf("wg0", []net.Addr{
		ipnet(t, "fe80::1/64"), ipnet(t, "203.0.113.5/24"), v4in6, ipnet(t, "192.168.1.10/24"), ipnet(t, "fd00::1/64"),
		&net.IPAddr{IP: net.ParseIP("10.9.9.9")}, // not an address with a prefix
	})
	want := Iface{Name: "wg0", Addrs: []Addr{
		{Iface: "wg0", CIDR: "10.8.0.2/32", Private: true},
		{Iface: "wg0", CIDR: "192.168.1.10/24", Private: true},
		{Iface: "wg0", CIDR: "203.0.113.5/24"},
		{Iface: "wg0", CIDR: "fd00::1/64", Private: true},
		{Iface: "wg0", CIDR: "fe80::1/64", Private: true},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ifaceOf:\n got %+v\nwant %+v", got, want)
	}
}

func TestPrivateRanges(t *testing.T) {
	for cidr, want := range map[string]bool{
		"10.255.255.254/8": true, "172.16.0.1/12": true, "172.31.255.1/12": true, "172.32.0.1/16": false,
		"192.168.0.9/16": true, "192.169.0.1/16": false, "169.254.1.1/16": false, "100.64.0.1/10": false,
		"203.0.113.5/24": false, "fc00::5/64": true, "fdff::1/64": true, "fe80::1/64": true, "febf::1/64": true,
		"fec0::1/64": false, "2001:db8::1/64": false,
	} {
		if got := ifaceOf("x", []net.Addr{ipnet(t, cidr)}).Addrs[0].Private; got != want {
			t.Errorf("%s private = %v, want %v", cidr, got, want)
		}
	}
}

func TestBridgeNames(t *testing.T) {
	for n, want := range map[string]bool{
		"docker0": true, "br-1a2b3c": true, "virbr0": true, "virbr0-nic": true, "veth12ab": true,
		"eth0": false, "wg0": false, "br0": false, "docker1": false, "enp3s0": false,
	} {
		if got := ifaceOf(n, nil).Bridge; got != want {
			t.Errorf("Bridge(%s) = %v, want %v", n, got, want)
		}
	}
}

func TestSystemInterfaces(t *testing.T) {
	ifs, err := SystemInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(ifs))
	for n, i := range ifs {
		names[n] = i.Name
		if i.Name == "lo" {
			t.Error("the loopback interface is listed")
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("not sorted: %v", names)
	}
}

func TestDefaultInterfaces(t *testing.T) {
	a := func(cidr string, priv bool) Addr { return Addr{CIDR: cidr, Private: priv} }
	ifs := []Iface{
		{Name: "eth0", Addrs: []Addr{a("192.168.1.10/24", true), a("fe80::1/64", true)}},
		{Name: "eth1", Addrs: []Addr{a("203.0.113.5/24", false)}},                            // public
		{Name: "eth2", Addrs: []Addr{a("192.168.2.2/24", true), a("2001:db8::1/64", false)}}, // one public address
		{Name: "ppp0", Addrs: []Addr{a("198.51.100.7/32", false), a("fe80::2/64", true)}},    // public and link-local
		{Name: "wg0", Addrs: []Addr{a("10.8.0.1/32", true)}},                                 // a /32
		{Name: "lo", Addrs: []Addr{a("10.0.0.1/8", true)}},
		{Name: "docker0", Bridge: true, Addrs: []Addr{a("172.17.0.1/16", true)}},
		{Name: "virbr0", Bridge: true, Addrs: []Addr{a("192.168.122.1/24", true)}},
		{Name: "veth12ab", Bridge: true, Addrs: []Addr{a("fe80::3/64", true)}},
		{Name: "br-1a2b", Bridge: true, Addrs: []Addr{a("172.18.0.1/16", true)}},
		{Name: "down"},
	}
	if got, want := DefaultInterfaces(ifs), []string{"eth0", "wg0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultInterfaces = %v, want %v", got, want)
	}
	if got := DefaultInterfaces(nil); len(got) != 0 {
		t.Errorf("DefaultInterfaces(nil) = %v", got)
	}
}

// The networks of the bridges are what hosts allow excepts and check-share refuses: each address of a bridge that is not
// link-local, as its network, once, IPv4 first; a chosen bridge has none. br0, the LAN bridge that ext-kvm makes, is no
// bridge of Docker or libvirt by the name rule, so its network is never among them.
func TestBridgeSubnets(t *testing.T) {
	ifs := []Iface{
		ifaceOf("br-1a2b", []net.Addr{ipnet(t, "172.18.0.1/16"), ipnet(t, "fd00:dead::1/64")}),
		ifaceOf("br0", []net.Addr{ipnet(t, "192.168.1.2/24")}),
		ifaceOf("docker0", []net.Addr{ipnet(t, "172.17.0.1/16"), ipnet(t, "fe80::42:1/64")}),
		ifaceOf("eth0", []net.Addr{ipnet(t, "192.168.1.10/24")}),
		ifaceOf("veth12ab", []net.Addr{ipnet(t, "fe80::3/64")}),
		ifaceOf("virbr0", []net.Addr{ipnet(t, "192.168.122.1/24")}),
		ifaceOf("virbr0-nic", []net.Addr{ipnet(t, "192.168.122.7/24")}), // the same network again
	}
	str := func(ps []netip.Prefix) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.String())
		}
		return strings.Join(out, " ")
	}
	if got, want := str(BridgeSubnets(ifs, nil)), "172.17.0.0/16 172.18.0.0/16 192.168.122.0/24 fd00:dead::/64"; got != want {
		t.Errorf("BridgeSubnets = %s, want %s", got, want)
	}
	if got, want := str(BridgeSubnets(ifs, []string{"virbr0", "virbr0-nic", "eth0"})), "172.17.0.0/16 172.18.0.0/16 fd00:dead::/64"; got != want {
		t.Errorf("BridgeSubnets with virbr0 chosen = %s, want %s", got, want)
	}
	if got := BridgeSubnets([]Iface{ifs[1], ifs[3]}, nil); len(got) != 0 {
		t.Errorf("BridgeSubnets of br0 and eth0 = %v", got)
	}
}

// An IPv4 address on a 16 byte IP with a 16 byte mask (the form some Go versions return) keeps its IPv4 prefix length.
func TestIfaceOfWideIPv4(t *testing.T) {
	got := ifaceOf("eth0", []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.1.10").To16(), Mask: net.CIDRMask(120, 128)}})
	if len(got.Addrs) != 1 || got.Addrs[0].CIDR != "192.168.1.10/24" {
		t.Errorf("addrs = %+v", got.Addrs)
	}
}
