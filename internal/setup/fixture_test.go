package setup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// The paths of the tools on the test host.
var tools = host.Tools{
	Smbd: "/usr/sbin/smbd", Smbpasswd: "/usr/bin/smbpasswd", Pdbedit: "/usr/bin/pdbedit", Sharesec: "/usr/bin/sharesec",
	Smbstatus: "/usr/bin/smbstatus", Smbcontrol: "/usr/bin/smbcontrol", Testparm: "/usr/bin/testparm",
	RcUpdate: "/sbin/rc-update", RcService: "/sbin/rc-service", Lbu: "/usr/sbin/lbu", Busybox: "/bin/busybox",
	Install: "/usr/bin/install", Rm: "/bin/rm",
}

const (
	self   = "/proc/self/exe"
	binary = "/usr/local/bin/edelweiss-ext-samba"
)

// The mounts of a host installed to disk, and of one that runs from RAM.
const (
	sysMounts      = "/dev/sda1 / ext4 rw 0 0\n"
	disklessMounts = "tmpfs / tmpfs rw 0 0\n"
)

func put(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// link makes the link that OpenRC keeps for a service: in the runlevel (etc/runlevels/default) or started
// (run/openrc/started).
func link(t *testing.T, root, dir, svc string) {
	t.Helper()
	p := filepath.Join(root, dir, svc)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/init.d/"+svc, p); err != nil {
		t.Fatal(err)
	}
}

func enable(t *testing.T, root string, svcs ...string) {
	for _, s := range svcs {
		link(t, root, "etc/runlevels/default", s)
	}
}

func start(t *testing.T, root string, svcs ...string) {
	for _, s := range svcs {
		link(t, root, "run/openrc/started", s)
	}
}

func captured(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../samba/testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// capturedWsdd is /etc/conf.d/wsdd of the package, as services.txt has it.
func capturedWsdd(t *testing.T) string {
	t.Helper()
	text := captured(t, "services.txt")
	_, after, ok := strings.Cut(text, "--- /etc/conf.d/wsdd\n")
	conf, _, ok2 := strings.Cut(after, "--- /etc/rc.conf")
	if !ok || !ok2 {
		t.Fatal("services.txt has no /etc/conf.d/wsdd")
	}
	return conf
}

// avahiConf stands for /etc/avahi/avahi-daemon.conf of the package (not captured): its [server] section with the
// commented interface lines, then other sections.
const avahiConf = `[server]
#host-name=foo
#domain-name=local
use-ipv4=yes
use-ipv6=yes
#allow-interfaces=eth0
#deny-interfaces=eth1
ratelimit-interval-usec=1000000

[wide-area]
enable-wide-area=yes

[publish]
#publish-aaaa-on-ipv4=yes
`

// The interfaces of the test host: a private eth0, a public eth1, a private VPN wg0, mixed0 with a private and a
// public address, and a Docker bridge.
var ifaces = []samba.Iface{
	{Name: "docker0", Bridge: true, Addrs: []samba.Addr{{Iface: "docker0", CIDR: "172.17.0.1/16", Private: true}}},
	{Name: "eth0", Addrs: []samba.Addr{{Iface: "eth0", CIDR: "192.168.1.10/24", Private: true}, {Iface: "eth0", CIDR: "fe80::1/64", Private: true}}},
	{Name: "eth1", Addrs: []samba.Addr{{Iface: "eth1", CIDR: "203.0.113.5/24"}}},
	{Name: "mixed0", Addrs: []samba.Addr{{Iface: "mixed0", CIDR: "192.168.2.1/24", Private: true}, {Iface: "mixed0", CIDR: "198.51.100.1/24"}}},
	{Name: "wg0", Addrs: []samba.Addr{{Iface: "wg0", CIDR: "10.8.0.1/24", Private: true}}},
}

// newHost is a host below a temp root, installed to disk, with the files the packages bring: the smb.conf of Samba's
// package, its /etc/conf.d/samba, /etc/conf.d/wsdd and avahi-daemon.conf.
func newHost(t *testing.T) (string, setup.Host) {
	t.Helper()
	root := t.TempDir()
	put(t, root, "proc/mounts", sysMounts)
	put(t, root, "etc/samba/smb.conf", captured(t, "smb.conf.default"))
	put(t, root, "etc/conf.d/samba", captured(t, "conf.d-samba"))
	put(t, root, setup.WsddConfD, capturedWsdd(t))
	put(t, root, setup.AvahiConf, avahiConf)
	return root, setup.Host{FS: host.FS{Root: root}, Tools: tools, Mode: host.Sys, Self: self, Binary: binary, Ifaces: ifaces}
}

// on is the discovery on, on eth0.
var on = samba.Settings{Discovery: true, Interfaces: []string{"eth0"}}

// wanted writes the files of wsdd and avahi-daemon as the discovery on the interfaces wants them.
func wanted(t *testing.T, root string, d ...string) {
	t.Helper()
	opts := "-4"
	for _, i := range d {
		opts += " -i " + i
	}
	put(t, root, setup.WsddConfD, `WSDD_OPTS="`+opts+`"`+"\n")
	put(t, root, setup.AvahiConf, "[server]\nallow-interfaces="+strings.Join(d, ",")+"\nuse-ipv6=no\n")
}

// ready is a host that the setup made, with the discovery on eth0: every item is OK but the ones a test breaks.
func ready(t *testing.T, root string) {
	t.Helper()
	ours(t, root, samba.Config{Settings: on})
	put(t, root, setup.SambaConfD, `daemon_list="smbd"`+"\n")
	put(t, root, "etc/group", "edelweiss-samba:x:103:\n")
	wanted(t, root, "eth0")
	script, err := recycle.CleanupScript(binary)
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, recycle.ScriptPath, string(script))
	if err := chmod(root, recycle.ScriptPath, 0o755); err != nil {
		t.Fatal(err)
	}
	enable(t, root, "dbus", "avahi-daemon", "samba", "wsdd", "crond")
	start(t, root, "dbus", "avahi-daemon", "samba", "wsdd", "crond")
}

func chmod(root, rel string, mode os.FileMode) error { return os.Chmod(filepath.Join(root, rel), mode) }

func removeLink(root, rel string) error { return os.Remove(filepath.Join(root, rel)) }

func describe(steps []jobs.Step) []string {
	out := []string{}
	for _, s := range steps {
		out = append(out, jobs.Describe(s))
	}
	return out
}
