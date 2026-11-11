//go:build integration

package integration

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// TestHostsAllow is Review Focus 4 with a public source and a bridge of Docker (NET_ADMIN). Beside the loopback address,
// on which smbd listens (interfaces has lo), lo gets 198.51.100.7, a public address (TEST-NET-2), and <net>.0.5, an
// address in the network <net>.0.0/16 of a bridge docker0 that comes later; a client that connects to one of them comes
// from it. <net> is 172.17, the network of Docker's docker0, unless the eth0 of the container is in it (it is on the
// docker0 network of this machine, interfaces.txt): then the next 172.x that eth0 is not in.
//
// hosts allow refuses the public source, as smbd's log says. The client of the bridge's network is let in while there is
// no bridge (the control). Then docker0 is made, after the last change of smb.conf: hosts allow does not know it, and the
// root preexec (check-share --client) refuses the client at the share, as smbd's log says. The next change of the config
// excepts the bridge's network in hosts allow, testparm takes the line, and smbd refuses the client at the connect. From
// the private address of eth0 and from the loopback address a client is let in all along.
func TestHostsAllow(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	const public = "198.51.100.7"
	const allow = "hosts allow = 127.0.0.1 ::1 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7 fe80::/10"
	text := e.result(e.tool("apk"), "add", "--quiet", "iproute2")
	ip := e.tool("ip")
	network := e.bridgeNetwork()
	client := strings.TrimSuffix(network, ".0.0/16") + ".0.5"
	e.step(ip, "addr", "add", public+"/32", "dev", "lo")
	e.step(ip, "addr", "add", client+"/32", "dev", "lo")
	e.linuxUser("ben", 1000, pwBen)
	e.setup()
	e.sambaUser("anna", pwAnna)
	e.dir(fotos, 1000, 1000, 0o755)
	e.addShare(samba.Share{Name: "Fotos", Path: fotos, Rights: map[string]samba.Right{"anna": samba.Write}})
	conf := fileText(smbConf)
	if !strings.Contains(conf, "\t"+allow+"\n") || !strings.Contains(conf, "\tinterfaces = eth0 lo\n") {
		t.Errorf("smb.conf:\n%s", conf)
	}
	for _, addr := range []string{public, client} {
		if !listening(addr + ":445") {
			t.Fatalf("smbd does not listen on %s:445:\n%s", addr, e.sh("netstat -ltn"))
		}
	}
	logs := func() string { time.Sleep(time.Second); return e.sh("cat /var/log/samba/log.* 2>/dev/null; true") }
	ok, out := e.smbAt(public, "Fotos", "anna", pwAnna, "ls")
	text += out
	if ok || !strings.Contains(logs(), "Denied connection from "+public) {
		t.Errorf("a client from %s: let in %v, or smbd's log does not say that it denied it:\n%s", public, ok, logs())
	}
	letIn := func(when string, addrs ...string) {
		for _, addr := range addrs {
			ok, out := e.smbAt(addr, "Fotos", "anna", pwAnna, "ls")
			text += out
			if !ok {
				t.Errorf("%s: a client from %s was refused", when, addr)
			}
		}
	}
	letIn("before the bridge", client, e.eth0(), "127.0.0.1")

	e.step(ip, "link", "add", "docker0", "type", "bridge")
	e.step(ip, "addr", "add", strings.TrimSuffix(network, ".0.0/16")+".0.1/16", "dev", "docker0")
	e.step(ip, "link", "set", "docker0", "up")
	text += e.sh("ip -o addr show")
	ok, out = e.smbAt(client, "Fotos", "anna", pwAnna, "ls")
	text += out
	reason := "check-share: Fotos: the client " + client + " is in " + network + ", a network of a Docker or libvirt bridge"
	if ok || !strings.Contains(out, "tree connect failed: NT_STATUS_ACCESS_DENIED") || !strings.Contains(logs(), reason) {
		t.Errorf("a client of the new bridge: let in %v, or not refused at the share with %q:\n%s\n%s", ok, reason, out, logs())
	}
	letIn("with the bridge, before the next change", e.eth0(), "127.0.0.1")

	e.settings(func(s *samba.Settings) { s.Workgroup = "HEIM" })
	if _, ok := poll(30*time.Second, 200*time.Millisecond, func() bool { return listening(client + ":445") }); !ok {
		t.Fatalf("smbd does not listen on %s:445 after the change", client)
	}
	conf = fileText(smbConf)
	if !strings.Contains(conf, "\t"+allow+" EXCEPT "+network+"\n") {
		t.Errorf("smb.conf after the change does not except %s:\n%s", network, conf)
	}
	parm, status := e.run(e.tool("testparm"), "-s", smbConf)
	text += "--- testparm -s\n" + withStatus(parm, status)
	if status != 0 || !strings.Contains(parm, "Loaded services file OK.") || !strings.Contains(parm, "EXCEPT") ||
		strings.Contains(parm, "nknown parameter") {
		t.Errorf("testparm -s of the line with EXCEPT: %d\n%s", status, parm)
	}
	ok, out = e.smbAt(client, "Fotos", "anna", pwAnna, "ls")
	text += out
	if ok || !strings.Contains(logs(), "Denied connection from "+client) {
		t.Errorf("a client of the bridge after the change: let in %v, or smbd's log does not say that it denied it:\n%s", ok, logs())
	}
	letIn("after the change", e.eth0(), "127.0.0.1")
	block("hosts-allow", text+e.sh("ip -o addr show lo; netstat -ltn | grep ':445 '; grep -h -i -e denied -e check-share /var/log/samba/log.*; true"))
}

// bridgeNetwork is the network of the bridge of TestHostsAllow: 172.17.0.0/16, or the first 172.x.0.0/16 up to 172.31 that
// the address of eth0 is not in.
func (e *e2e) bridgeNetwork() string {
	e.t.Helper()
	eth0 := netip.MustParseAddr(e.eth0())
	for x := 17; x <= 31; x++ {
		p := netip.MustParsePrefix(fmt.Sprintf("172.%d.0.0/16", x))
		if !p.Contains(eth0) {
			return p.String()
		}
	}
	e.t.Fatalf("eth0 (%s) is in every network of 172.17 to 172.31", eth0)
	return ""
}

// TestDiscoveryInterfaces: avahi-daemon.conf as the packages ship it (internal/samba/testdata), then a second interface
// lan1 with a private address beside eth0 (NET_ADMIN; a veth, whose peer veth-lan1 the extension takes for a bridge).
// The setup takes both; wsdd and avahi-daemon answer on both (the control). With only eth0 chosen they answer on eth0
// alone: no wsdd socket on lan1's address, and avahi joins mDNS on eth0 only. With none of the local interfaces chosen,
// both stop and leave the runlevel, and smb.conf has multicast dns register = no, which testparm takes.
func TestDiscoveryInterfaces(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	block("avahi-daemon.conf", fileText(setup.AvahiConf))
	block("conf.d-wsdd", fileText(setup.WsddConfD))
	const lan1 = "10.99.0.1"
	text := e.result(e.tool("apk"), "add", "--quiet", "iproute2")
	ip := e.tool("ip")
	e.step(ip, "link", "add", "lan1", "type", "veth", "peer", "name", "veth-lan1")
	e.step(ip, "addr", "add", lan1+"/24", "dev", "lan1")
	e.step(ip, "link", "set", "veth-lan1", "up")
	e.step(ip, "link", "set", "lan1", "up")
	text += e.sh("ip -o addr show")
	e.linuxUser("ben", 1000, pwBen)
	e.setup()
	eth0 := e.eth0()
	if got := e.config().Settings.Interfaces; !slices.Equal(got, []string{"eth0", "lan1"}) {
		t.Fatalf("the interfaces of the setup: %v", got)
	}
	// wsdd answers on an address with HTTP (TCP 5357) and WS-Discovery (UDP 3702); one netstat snapshot per sample, so that
	// a sample is one moment
	wsdd := func(out, addr string) bool {
		return strings.Contains(out, " "+addr+":5357 ") && strings.Contains(out, " "+addr+":3702 ")
	}
	none := func(out, addr string) bool {
		return !strings.Contains(out, " "+addr+":5357 ") && !strings.Contains(out, " "+addr+":3702 ")
	}
	sample := func() string { return e.sh("netstat -ltnu") }
	if _, ok := poll(20*time.Second, 200*time.Millisecond, func() bool { out := sample(); return wsdd(out, eth0) && wsdd(out, lan1) }); !ok {
		t.Fatalf("wsdd does not answer on both interfaces after the setup:\n%s", e.sh("netstat -ltnu"))
	}
	logged := fileText("/var/log/messages")
	if !strings.Contains(logged, "interface lan1.IPv4") || !strings.Contains(logged, "interface eth0.IPv4") {
		t.Errorf("avahi-daemon does not answer on both interfaces after the setup:\n%s", logged)
	}
	text += "--- both\n" + e.sh("netstat -ltnu; cat "+setup.WsddConfD+"; grep -v '^#' "+setup.AvahiConf+" | grep .")

	mark := len(fileText("/var/log/messages"))
	e.settings(func(s *samba.Settings) { s.Interfaces = []string{"eth0"} })
	if _, ok := poll(20*time.Second, 200*time.Millisecond, func() bool { out := sample(); return wsdd(out, eth0) && none(out, lan1) }); !ok {
		t.Errorf("wsdd with only eth0 chosen:\n%s", sample())
	}
	time.Sleep(2 * time.Second)
	// settled: a wsdd that bound its interfaces one after the other would have bound lan1 by now
	if out := sample(); !wsdd(out, eth0) || !none(out, lan1) {
		t.Errorf("wsdd 2 s after the change to eth0 alone:\n%s", out)
	}
	// what the avahi-daemon that the change restarted logged (the one before may log its leave of lan1 after the mark)
	logged = fileText("/var/log/messages")[mark:]
	restarted := ""
	if i := strings.LastIndex(logged, " starting up."); i >= 0 {
		restarted = logged[i:]
	}
	if !strings.Contains(restarted, "Joining mDNS multicast group on interface eth0.IPv4") || strings.Contains(restarted, "lan1") {
		t.Errorf("avahi-daemon with only eth0 chosen:\n%s", logged)
	}
	if v := fileText(setup.WsddConfD); !strings.Contains(v, `WSDD_OPTS="-4 -i eth0"`) {
		t.Errorf("%s:\n%s", setup.WsddConfD, v)
	}
	text += "--- eth0\n" + e.sh("netstat -ltnu; cat "+setup.WsddConfD+"; grep -v '^#' "+setup.AvahiConf+" | grep .") + logged +
		e.result(e.tool("avahi-daemon"), "--check")

	e.settings(func(s *samba.Settings) { s.Interfaces = nil })
	// smbd does not announce itself through an avahi then, which testparm takes
	parm, status := e.run(e.tool("testparm"), "-s", smbConf)
	if status != 0 || !strings.Contains(strings.ToLower(parm), "multicast dns register = no") || strings.Contains(parm, "nknown parameter") {
		t.Errorf("testparm -s with no local interface: %d\n%s", status, parm)
	}
	v := e.view()
	for _, svc := range []string{"wsdd", "avahi-daemon"} {
		if v.Services[svc] || exists("/etc/runlevels/default/"+svc) {
			t.Errorf("%s with no local interface chosen: started %v, in the runlevel %v", svc, v.Services[svc], exists("/etc/runlevels/default/"+svc))
		}
	}
	if ps := e.sh("ps -o args | grep -E 'wsdd|avahi-daemon' | grep -v grep; true"); ps != "" {
		t.Errorf("still running: %s", ps)
	}
	if !slices.ContainsFunc(v.Setup.Items, func(it setup.Item) bool { return it.ID == "discovery" && it.Detail == "no_local_interface" }) {
		t.Errorf("the setup items with no local interface: %+v", v.Setup.Items)
	}
	block("discovery", text+"--- none\n"+e.sh("netstat -ltnu; ls /etc/runlevels/default"))
}
