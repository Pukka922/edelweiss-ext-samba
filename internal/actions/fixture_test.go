package actions_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// The job the plans are for, and the smb.conf it stages.
const (
	jobID  = "20261104T120000-abcd"
	jobDir = "/run/edelweiss-ext-samba/jobs/" + jobID
	staged = jobDir + "/smb.conf"
)

const (
	self   = "/proc/self/exe"
	binary = "/usr/local/bin/edelweiss-ext-samba"
)

// The SIDs of the Samba users of the tests (pdbedit -L -v).
const (
	sidAnna = "S-1-5-21-1-2-3-1000"
	sidBen  = "S-1-5-21-1-2-3-1001"
	sidDora = "S-1-5-21-1-2-3-1003"
)

var tools = host.Tools{
	Smbd: "/usr/sbin/smbd", Smbpasswd: "/usr/bin/smbpasswd", Pdbedit: "/usr/bin/pdbedit", Sharesec: "/usr/bin/sharesec",
	Smbstatus: "/usr/bin/smbstatus", Smbcontrol: "/usr/bin/smbcontrol", Testparm: "/usr/bin/testparm",
	RcUpdate: "/sbin/rc-update", RcService: "/sbin/rc-service", Lbu: "/usr/sbin/lbu", Busybox: "/bin/busybox",
	Install: "/usr/bin/install", Rm: "/bin/rm",
}

// The lines of the steps that every change of the config has.
const (
	writeStaged = "write " + staged
	testparm    = "exec /usr/bin/testparm -s " + staged
	writeConfig = "write /etc/edelweiss-ext-samba/config.json"
	writeConf   = "write /etc/samba/smb.conf"
	reload      = "exec /usr/bin/smbcontrol smbd reload-config"
	lbuCommit   = "exec /usr/sbin/lbu commit"
	writeWsdd   = "write /etc/conf.d/wsdd"
	writeAvahi  = "write /etc/avahi/avahi-daemon.conf"
)

// shareACL is the sharesec step of a share, against the staged smb.conf.
func shareACL(share, entries string) string {
	return "exec /usr/bin/sharesec --configfile=" + staged + " " + share + " --replace " + entries
}

func tool(args ...string) string { return "exec " + self + " --samba-tool " + strings.Join(args, " ") }

const (
	annaWrite = sidAnna + ":ALLOWED/0/CHANGE"
	benRead   = sidBen + ":ALLOWED/0/READ"
)

// fixture is a host below a temp root: / on sda1, the data disk /mnt/disk (ext4) with the folder data (and sub,
// sub/deep, other in it) and the stick /mnt/stick (vfat), the folders owned by the user of the test as myUser:myGroup,
// Samba set up with an smb.conf of the extension, samba and its friends running.
type fixture struct {
	t    *testing.T
	root string
	env  actions.Env
	fsid string // of /mnt/disk
}

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

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	put(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n/dev/sdc1 /mnt/stick vfat rw 0 0\n")
	for _, d := range []string{"mnt/disk/data/sub/deep", "mnt/disk/data/other", "mnt/disk/more", "mnt/stick/files"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	uid, gid := os.Getuid(), os.Getgid()
	put(t, root, accounts.PasswdFile, "myUser:x:"+itoa(uid)+":"+itoa(gid)+"::/home/myUser:/bin/ash\n")
	put(t, root, accounts.GroupFile, "myGroup:x:"+itoa(gid)+":\nedelweiss-samba:x:103:\n")
	put(t, root, samba.ConfPath, "# Made by edelweiss-ext-samba. Changes are overwritten.\n")
	put(t, root, "etc/conf.d/samba", "daemon_list=\"smbd\"\n")
	put(t, root, setup.WsddConfD, `WSDD_OPTS="-4 -i eth0"`+"\n")
	put(t, root, setup.AvahiConf, "[server]\nallow-interfaces=eth0\nuse-ipv6=no\n")
	fs := host.FS{Root: root}
	mounts, err := fs.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	data, err := paths.DataMounts(fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	fsid, err := paths.FsidOf(fs, "/mnt/disk")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, fsid: fsid}
	f.services("dbus", "avahi-daemon", "samba", "wsdd", "crond")
	f.env = actions.Env{
		FS: fs, Tools: tools, Mode: host.Sys, SetUp: true,
		Config: samba.Config{Version: samba.ConfigVersion, Settings: samba.Settings{Discovery: true, Interfaces: []string{"eth0"}},
			Shares: []samba.Share{}},
		SambaUsers: []samba.SambaUser{{Name: "anna", SID: sidAnna}, {Name: "ben", SID: sidBen}, {Name: "dora", SID: sidDora}},
		DB: &accounts.DB{Users: []accounts.User{{Name: "myUser", UID: uid, GID: gid}},
			Groups: []accounts.Group{{Name: "myGroup", GID: gid}}},
		Mounts: mounts, Data: data,
		Ifaces: []samba.Iface{
			{Name: "eth0", Addrs: []samba.Addr{{Iface: "eth0", CIDR: "192.168.1.10/24", Private: true}}},
			{Name: "eth1", Addrs: []samba.Addr{{Iface: "eth1", CIDR: "203.0.113.5/24"}}},
			{Name: "docker0", Bridge: true, Addrs: []samba.Addr{{Iface: "docker0", CIDR: "172.17.0.1/16", Private: true}}},
			{Name: "wg0", Addrs: []samba.Addr{{Iface: "wg0", CIDR: "10.8.0.1/24", Private: true}}},
		},
		Binary: binary, Self: self, JobDir: jobDir,
		Probe: func(samba.Share) error { return nil },
	}
	return f
}

func itoa(n int) string { return strconv.Itoa(n) }

// services puts the services into the runlevel and has OpenRC show them started.
func (f *fixture) services(svcs ...string) {
	for _, s := range svcs {
		for _, dir := range []string{"etc/runlevels/default", "run/openrc/started"} {
			p := filepath.Join(f.root, dir, s)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				f.t.Fatal(err)
			}
			if err := os.Symlink("/etc/init.d/"+s, p); err != nil && !errors.Is(err, os.ErrExist) {
				f.t.Fatal(err)
			}
		}
	}
}

// stop has OpenRC show the services stopped.
func (f *fixture) stop(svcs ...string) {
	for _, s := range svcs {
		if err := os.Remove(filepath.Join(f.root, "run/openrc/started", s)); err != nil {
			f.t.Fatal(err)
		}
	}
}

// fotos is the share Fotos of the folder data on the disk: anna writes, ben reads.
func (f *fixture) fotos() samba.Share {
	return samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Mount: "/mnt/disk", Fsid: f.fsid,
		Rights: map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}}
}

func (f *fixture) with(shares ...samba.Share) { f.env.Config.Shares = shares }

func (f *fixture) build(r actions.Request) (jobs.Spec, error) { return actions.Build(r, f.env) }

func (f *fixture) plan(r actions.Request) jobs.Spec {
	f.t.Helper()
	spec, err := f.build(r)
	if err != nil {
		f.t.Fatalf("%s: %v", r.Kind, err)
	}
	return spec
}

// refused is the refusal of the request; it fails the test when Build plans it.
func (f *fixture) refused(r actions.Request) *actions.Error {
	f.t.Helper()
	_, err := f.build(r)
	var e *actions.Error
	if !errors.As(err, &e) {
		f.t.Fatalf("%s: %v, want a refusal", r.Kind, err)
	}
	return e
}

func describe(steps []jobs.Step) []string {
	out := []string{}
	for _, s := range steps {
		out = append(out, jobs.Describe(s))
	}
	return out
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %s\nwant %s", what, strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func step(t *testing.T, steps []jobs.Step, desc string) jobs.Step {
	t.Helper()
	i := slices.IndexFunc(steps, func(s jobs.Step) bool { return jobs.Describe(s) == desc })
	if i < 0 {
		t.Fatalf("no step %q in\n%s", desc, strings.Join(describe(steps), "\n"))
	}
	return steps[i]
}

// written is the config that the plan writes.
func written(t *testing.T, spec jobs.Spec) samba.Config {
	t.Helper()
	var c samba.Config
	if err := json.Unmarshal(step(t, spec.Steps, writeConfig).Data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func code(t *testing.T, e *actions.Error, want string, details map[string]any) {
	t.Helper()
	if e.Code != want {
		t.Errorf("code %q (%s), want %q", e.Code, e.Message, want)
	}
	for k, v := range details {
		if !reflect.DeepEqual(e.Details[k], v) {
			t.Errorf("%s: details[%s] = %#v, want %#v", want, k, e.Details[k], v)
		}
	}
}
