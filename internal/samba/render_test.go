package samba_test

import (
	"flag"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	confPath = "/etc/samba/smb.conf"
	binary   = "/usr/local/bin/edelweiss-ext-samba"
)

func baseEnv() samba.RenderEnv {
	return samba.RenderEnv{
		Owners: map[string]samba.Owner{
			"Fotos":     {User: "myUser", Group: "myUser"},
			"Archiv":    {User: "myUser", Group: "myUser"},
			"Dokumente": {User: "myUser", Group: "familie"},
		},
		SambaUsers: map[string]bool{"myUser": true, "anna": true, "ben": true},
		Binary:     binary,
	}
}

// Fotos: a plain share with a recycle bin (one writer, two readers). Archiv: hidden. Dokumente: converted for folder
// rights, with a recycle bin.
func baseConfig() samba.Config {
	return samba.Config{
		Version:  1,
		Settings: samba.Settings{Name: "NAS", Workgroup: "HOME", Interfaces: []string{"eth0", "wg0"}},
		Shares: []samba.Share{
			{Name: "Fotos", Path: "/srv/fotos", Comment: "Familienbilder", Mount: "/srv", Recycle: true, RecycleDays: 30,
				Rights: map[string]samba.Right{"myUser": samba.Write, "anna": samba.Read, "ben": samba.Read}},
			{Name: "Archiv", Path: "/srv/archiv", Hidden: true,
				Rights: map[string]samba.Right{"myUser": samba.Write, "anna": samba.Read}},
			{Name: "Dokumente", Path: "/srv/dokumente", Recycle: true, RecycleDays: 7, PerUser: true,
				Rights:  map[string]samba.Right{"myUser": samba.Write, "anna": samba.Write, "ben": samba.Read},
				Folders: map[string]map[string]samba.Right{"privat": {"anna": samba.Write}}},
		},
	}
}

// render is Render that fails the test on an error and returns the files.
func render(t *testing.T, c samba.Config, env samba.RenderEnv) map[string]string {
	t.Helper()
	res, err := samba.Render(c, env)
	if err != nil {
		t.Fatal(err)
	}
	return res.Files
}

// dump is the files of a render as one text: each file below a comment line with its path, in path order.
func dump(files map[string]string) string {
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var b strings.Builder
	for _, p := range paths {
		b.WriteString("# ---- " + p + " ----\n" + files[p])
	}
	return b.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.conf")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the render (rerun with -update after reading the diff):\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// section is the lines of the section [name] of a smb.conf, without the header; nil when there is none.
func section(conf, name string) []string {
	var out []string
	in, found := false, false
	for _, l := range strings.Split(conf, "\n") {
		if strings.HasPrefix(l, "[") {
			in = l == "["+name+"]"
			found = found || in
			continue
		}
		if in && strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimPrefix(l, "\t"))
		}
	}
	if !found {
		return nil
	}
	return out
}

func hasLine(sec []string, line string) bool { return slices.Contains(sec, line) }

func hasKey(sec []string, key string) bool {
	for _, l := range sec {
		if strings.HasPrefix(l, key+" =") {
			return true
		}
	}
	return false
}

func TestRenderGolden(t *testing.T) {
	files := render(t, baseConfig(), baseEnv())
	golden(t, "render-mixed", dump(files))
	if render(t, baseConfig(), baseEnv())[confPath] != files[confPath] {
		t.Error("two renders of the same input differ")
	}
}

func TestRenderFilesOwned(t *testing.T) {
	files := render(t, baseConfig(), baseEnv())
	var got []string
	for p := range files {
		got = append(got, p)
	}
	slices.Sort(got)
	want := []string{
		samba.IncludePath("Dokumente", "ben"), samba.IncludePath("Fotos", "anna"),
		samba.IncludePath("Fotos", "ben"), confPath,
	}
	if !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestRenderGlobal(t *testing.T) {
	g := section(render(t, baseConfig(), baseEnv())[confPath], "global")
	for _, l := range []string{
		"server role = standalone server", "workgroup = HOME", "netbios name = NAS", "disable netbios = yes",
		"server min protocol = SMB3", "map to guest = never", "map archive = no", "load printers = no", "access based share enum = yes",
		"bind interfaces only = yes", "interfaces = eth0 lo wg0", "fruit:aapl = yes", "fruit:metadata = stream",
		"fruit:model = MacSamba", "fruit:nfs_aces = no", "fruit:posix_rename = yes", "fruit:veto_appledouble = no",
		"fruit:delete_empty_adfiles = yes", "fruit:wipe_intentionally_left_blank_rfork = yes",
		"hosts allow = 127.0.0.1 ::1 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7 fe80::/10",
	} {
		if !hasLine(g, l) {
			t.Errorf("[global] lacks %q:\n%s", l, strings.Join(g, "\n"))
		}
	}
	// Samba's own paths stay as compiled in: no relocation of the tdb files.
	for _, k := range []string{"private dir", "state directory", "lock directory", "cache directory", "passdb backend"} {
		if hasKey(g, k) {
			t.Errorf("[global] sets %q", k)
		}
	}
	// hosts allow alone limits (hosts-allow-loopback), so there is no deny line.
	if hasKey(g, "hosts deny") {
		t.Error("[global] has hosts deny")
	}
}

func TestRenderGlobalDefaults(t *testing.T) {
	g := section(render(t, samba.Config{}, samba.RenderEnv{Binary: binary})[confPath], "global")
	if !hasLine(g, "workgroup = WORKGROUP") || hasKey(g, "netbios name") || !hasLine(g, "interfaces = lo") {
		t.Errorf("defaults:\n%s", strings.Join(g, "\n"))
	}
}

func TestRenderSMB2Switch(t *testing.T) {
	c := baseConfig()
	if g := section(render(t, c, baseEnv())[confPath], "global"); !hasLine(g, "server min protocol = SMB3") {
		t.Errorf("SMB2 off: %v", g)
	}
	c.Settings.SMB2 = true
	g := section(render(t, c, baseEnv())[confPath], "global")
	if !hasLine(g, "server min protocol = SMB2_02") {
		t.Errorf("SMB2 on: %v", g)
	}
	for _, l := range g {
		if strings.Contains(l, "NT1") || strings.Contains(l, "SMB1") || strings.Contains(l, "CORE") {
			t.Errorf("SMB1 appears: %s", l)
		}
	}
}

// The review focus: eth0 192.168.1.10, wg0 10.8.0.1 (a /32 too), docker0, virbr0, a veth and eth1 with a public address.
func reviewHost() []samba.Iface {
	a := func(i, cidr string, priv bool) samba.Addr { return samba.Addr{Iface: i, CIDR: cidr, Private: priv} }
	return []samba.Iface{
		{Name: "br-1a2b3c", Bridge: true, Addrs: []samba.Addr{a("br-1a2b3c", "172.18.0.1/16", true)}},
		{Name: "docker0", Bridge: true, Addrs: []samba.Addr{a("docker0", "172.17.0.1/16", true)}},
		{Name: "eth0", Addrs: []samba.Addr{a("eth0", "192.168.1.10/24", true), a("eth0", "fe80::1/64", true)}},
		{Name: "eth1", Addrs: []samba.Addr{a("eth1", "203.0.113.5/24", false), a("eth1", "fe80::2/64", true)}},
		{Name: "veth12ab", Bridge: true, Addrs: []samba.Addr{a("veth12ab", "fe80::3/64", true)}},
		{Name: "virbr0", Bridge: true, Addrs: []samba.Addr{a("virbr0", "192.168.122.1/24", true)}},
		{Name: "wg0", Addrs: []samba.Addr{a("wg0", "10.8.0.1/32", true)}},
	}
}

func TestRenderInterfaces(t *testing.T) {
	chosen := samba.DefaultInterfaces(reviewHost())
	if want := []string{"eth0", "wg0"}; !slices.Equal(chosen, want) {
		t.Fatalf("DefaultInterfaces = %v, want %v", chosen, want)
	}
	c := baseConfig()
	c.Settings.Interfaces = chosen
	conf := render(t, c, baseEnv())[confPath]
	g := section(conf, "global")
	if !hasLine(g, "interfaces = eth0 lo wg0") || !hasLine(g, "bind interfaces only = yes") {
		t.Errorf("interfaces:\n%s", strings.Join(g, "\n"))
	}
	for _, bad := range []string{"docker0", "virbr0", "eth1", "veth", "br-", "203.0.113"} {
		if strings.Contains(conf, bad) {
			t.Errorf("the config names %q:\n%s", bad, conf)
		}
	}
	// lo is always there, once; the choice is used as it is.
	c.Settings.Interfaces = []string{"wg0", "lo", "wg0"}
	if g := section(render(t, c, baseEnv())[confPath], "global"); !hasLine(g, "interfaces = lo wg0") {
		t.Errorf("selected interfaces:\n%s", strings.Join(g, "\n"))
	}
	// A name that could carry a second parameter or a wildcard is not written.
	c.Settings.Interfaces = []string{"eth0", "eth0 hosts deny = x", "1.2.3.4/24", "*"}
	if g := section(render(t, c, baseEnv())[confPath], "global"); !hasLine(g, "interfaces = eth0 lo") {
		t.Errorf("odd interface names:\n%s", strings.Join(g, "\n"))
	}
}

// The review focus host: hosts allow excepts the networks of docker0, br-1a2b3c and virbr0 (Linux takes their packets for
// the address of eth0 too), and names no bridge, no public address and no link-local network of a veth.
func TestRenderHostsAllowReviewHost(t *testing.T) {
	c := baseConfig()
	c.Settings.Interfaces = samba.DefaultInterfaces(reviewHost())
	env := baseEnv()
	env.Ifaces = reviewHost()
	g := section(render(t, c, env)[confPath], "global")
	want := "hosts allow = 127.0.0.1 ::1 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7 fe80::/10 EXCEPT 172.17.0.0/16 172.18.0.0/16 192.168.122.0/24"
	if !hasLine(g, want) {
		t.Errorf("[global] lacks %q:\n%s", want, strings.Join(g, "\n"))
	}
}

// Two bridges, IPv4 only: hosts allow excepts both networks; a bridge that is chosen is never excepted, and without a
// bridge there is no EXCEPT.
func TestRenderHostsAllowExceptsBridges(t *testing.T) {
	const allow = "hosts allow = 127.0.0.1 ::1 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7 fe80::/10"
	env := baseEnv()
	env.Ifaces = []samba.Iface{
		{Name: "docker0", Bridge: true, Addrs: []samba.Addr{{Iface: "docker0", CIDR: "172.17.0.1/16", Private: true}}},
		{Name: "eth0", Addrs: []samba.Addr{{Iface: "eth0", CIDR: "192.168.1.10/24", Private: true}}},
		{Name: "virbr0", Bridge: true, Addrs: []samba.Addr{{Iface: "virbr0", CIDR: "192.168.122.1/24", Private: true}}},
	}
	c := baseConfig()
	c.Settings.Interfaces = []string{"eth0"}
	if g := section(render(t, c, env)[confPath], "global"); !hasLine(g, allow+" EXCEPT 172.17.0.0/16 192.168.122.0/24") {
		t.Errorf("two bridges:\n%s", strings.Join(g, "\n"))
	}
	c.Settings.Interfaces = []string{"eth0", "virbr0"}
	if g := section(render(t, c, env)[confPath], "global"); !hasLine(g, allow+" EXCEPT 172.17.0.0/16") {
		t.Errorf("virbr0 chosen:\n%s", strings.Join(g, "\n"))
	}
	env.Ifaces = env.Ifaces[1:2]
	if g := section(render(t, c, env)[confPath], "global"); !hasLine(g, allow) {
		t.Errorf("no bridge:\n%s", strings.Join(g, "\n"))
	}
}

// smbd announces itself through avahi (mDNS) only when the discovery is on, on a local interface (MDNS): otherwise
// multicast dns register = no, so that an avahi-daemon that runs for something else does not announce it.
func TestRenderMDNS(t *testing.T) {
	env := baseEnv()
	if g := section(render(t, baseConfig(), env)[confPath], "global"); !hasLine(g, "multicast dns register = no") {
		t.Errorf("no discovery:\n%s", strings.Join(g, "\n"))
	}
	env.MDNS = true
	if g := section(render(t, baseConfig(), env)[confPath], "global"); hasKey(g, "multicast dns register") {
		t.Errorf("the discovery on:\n%s", strings.Join(g, "\n"))
	}
}

// A new interface is never included by itself: the config names eth0, and wg0 shows up later.
func TestRenderNewInterfaceNotIncluded(t *testing.T) {
	c := baseConfig()
	c.Settings.Interfaces = []string{"eth0"}
	if g := section(render(t, c, baseEnv())[confPath], "global"); !hasLine(g, "interfaces = eth0 lo") {
		t.Errorf("a new interface is bound:\n%s", strings.Join(g, "\n"))
	}
}

// An empty choice is a valid one: lo only.
func TestRenderNoInterfacesChosen(t *testing.T) {
	c := baseConfig()
	c.Settings.Interfaces = nil
	if g := section(render(t, c, baseEnv())[confPath], "global"); !hasLine(g, "interfaces = lo") || !hasLine(g, "bind interfaces only = yes") {
		t.Errorf("no choice:\n%s", strings.Join(g, "\n"))
	}
}

func TestRenderDropsStaleNames(t *testing.T) {
	c := baseConfig()
	env := baseEnv()
	delete(env.SambaUsers, "ben") // ben was deleted
	files := render(t, c, env)
	if strings.Contains(files[confPath], "ben") {
		t.Errorf("a stale name is still in the config:\n%s", files[confPath])
	}
	for p := range files {
		if strings.Contains(p, "ben") {
			t.Errorf("a file for a stale name: %s", p)
		}
	}
	if sec := section(files[confPath], "Fotos"); !hasLine(sec, "valid users = anna myUser") || !hasLine(sec, "read list = anna") {
		t.Errorf("Fotos:\n%s", strings.Join(sec, "\n"))
	}

	// A share nobody remains in is left out: an empty valid users would let every account in.
	env.SambaUsers = map[string]bool{"zed": true}
	conf := render(t, c, env)[confPath]
	if section(conf, "Fotos") != nil || section(conf, "Archiv") != nil {
		t.Errorf("shares without a user are rendered:\n%s", conf)
	}
	// A right other than read and write does not count.
	c.Shares = []samba.Share{{Name: "Fotos", Path: "/srv/fotos", Rights: map[string]samba.Right{"ben": "", "anna": "admin"}}}
	env.SambaUsers = map[string]bool{"ben": true, "anna": true}
	if section(render(t, c, env)[confPath], "Fotos") != nil {
		t.Error("a share without rights is rendered")
	}
}

func TestRenderSkipReasons(t *testing.T) {
	rights := map[string]samba.Right{"myUser": samba.Write}
	env := baseEnv()
	own := func(name, user, group string) { env.Owners[name] = samba.Owner{User: user, Group: group} }
	own("Fotos", "myUser", "myUser")
	own("fotos", "myUser", "myUser")
	own("NoGroup", "myUser", "")
	own("RootOwned", "root", "root")
	own("Newline", "my\nUser", "myUser")
	own("global", "myUser", "myUser")
	own("a/b", "myUser", "myUser")
	own("Empty", "myUser", "myUser")
	for _, n := range []string{"RelPath", "Percent", "Backslash", "NL", "LeadSpace", "TrailSpace", "DoubleSpace", "Tab", "Dots"} {
		own(n, "myUser", "myUser")
	}
	delete(env.Owners, "Archiv")
	want := map[string]string{
		"fotos": samba.SkipCaseClash, "Archiv": samba.SkipNoOwner, "NoGroup": samba.SkipNoOwner, "Newline": samba.SkipNoOwner,
		"RootOwned": samba.SkipRootOwned, "global": samba.SkipBadName, "a/b": samba.SkipBadName,
		"Empty": samba.SkipNoUsers, "RelPath": samba.SkipBadPath, "Percent": samba.SkipBadPath, "Backslash": samba.SkipBadPath,
		"NL": samba.SkipBadPath, "LeadSpace": samba.SkipBadPath, "TrailSpace": samba.SkipBadPath,
		"DoubleSpace": samba.SkipBadPath, "Tab": samba.SkipBadPath, "Dots": samba.SkipBadPath,
	}
	c := samba.Config{Shares: []samba.Share{
		{Name: "Fotos", Path: "/srv/fotos", Rights: rights},
		{Name: "fotos", Path: "/srv/other", Rights: rights}, // the same name for Samba
		{Name: "Archiv", Path: "/srv/archiv", Rights: rights},
		{Name: "NoGroup", Path: "/srv/x", Rights: rights},
		{Name: "RootOwned", Path: "/srv/root", Rights: rights},
		{Name: "global", Path: "/srv/g", Rights: rights},
		{Name: "a/b", Path: "/srv/ab", Rights: rights},
		{Name: "Newline", Path: "/srv/nl", Rights: rights},
		{Name: "Empty", Path: "/srv/empty", Rights: map[string]samba.Right{"nobody": samba.Write, "myUser": ""}},
		{Name: "RelPath", Path: "relative", Rights: rights},
		{Name: "Percent", Path: "/srv/100%", Rights: rights},
		{Name: "Backslash", Path: `/srv/x\`, Rights: rights},
		{Name: "NL", Path: "/srv/f\n2", Rights: rights},
		{Name: "LeadSpace", Path: "/srv/ a", Rights: rights},
		{Name: "TrailSpace", Path: "/srv/a /b", Rights: rights},
		{Name: "DoubleSpace", Path: "/srv/a  b", Rights: rights},
		{Name: "Tab", Path: "/srv/a\tb", Rights: rights},
		{Name: "Dots", Path: "/srv/../etc", Rights: rights},
	}}
	res, err := samba.Render(c, env)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(res.Skipped, want) {
		t.Errorf("skipped = %v\n want %v", res.Skipped, want)
	}
	conf := res.Files[confPath]
	var names []string
	for _, l := range strings.Split(conf, "\n") {
		if strings.HasPrefix(l, "[") && l != "[global]" {
			names = append(names, l)
		}
	}
	if want := []string{"[Fotos]"}; !slices.Equal(names, want) {
		t.Errorf("sections = %v, want %v\n%s", names, want, conf)
	}
	if sec := section(conf, "Fotos"); !hasLine(sec, "path = /srv/fotos") {
		t.Errorf("the first of two names must win:\n%s", strings.Join(sec, "\n"))
	}
	// A path with a single space in a component is fine.
	c = samba.Config{Shares: []samba.Share{{Name: "Fotos", Path: "/srv/Meine Bilder/2024", Rights: rights}}}
	if sec := section(render(t, c, env)[confPath], "Fotos"); !hasLine(sec, "path = /srv/Meine Bilder/2024") {
		t.Errorf("a path with a space:\n%s", strings.Join(sec, "\n"))
	}
}

func TestRenderFailsWithoutUsableBinary(t *testing.T) {
	for _, bin := range []string{"", "relative/bin", "/a/../b", "/it's", "/a\nb", "/a\tb", "/a%b", "/a//b", "/a/"} {
		env := baseEnv()
		env.Binary = bin
		res, err := samba.Render(baseConfig(), env)
		if err == nil || len(res.Files) != 0 {
			t.Errorf("Binary %q: Render = (%v, %v), want an error and no files", bin, res.Files, err)
		}
	}
}

// smbd expands %U in lower case, so a reader named myUser needs the file of myuser.
func TestRenderRecycleMixedCaseReader(t *testing.T) {
	c := baseConfig()
	c.Shares = c.Shares[:1]
	c.Shares[0].Rights = map[string]samba.Right{"anna": samba.Write, "myUser": samba.Read}
	files := render(t, c, baseEnv())
	p := samba.IncludePath("Fotos", "myUser")
	if !strings.HasSuffix(p, "-myuser.conf") || files[p] != "veto files = /#recycle/\n" {
		t.Errorf("include of myUser: %q, files %v", p, files)
	}
	if strings.Contains(strings.Join(slices.Collect(maps.Keys(files)), "\n"), "myUser") {
		t.Errorf("a file name has the user's case: %v", files)
	}
}

// Two readers that differ only by case would share one file: neither gets it, and Skipped says so.
func TestRenderRecycleCaseClashOfReaders(t *testing.T) {
	c := baseConfig()
	c.Shares = c.Shares[:1]
	c.Shares[0].Rights = map[string]samba.Right{"myUser": samba.Write, "Anna": samba.Read, "anna": samba.Read, "ben": samba.Read}
	env := baseEnv()
	env.SambaUsers["Anna"] = true
	res, err := samba.Render(c, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Files[samba.IncludePath("Fotos", "anna")]; ok {
		t.Error("a file for a clashing pair")
	}
	if _, ok := res.Files[samba.IncludePath("Fotos", "ben")]; !ok {
		t.Error("no file for ben")
	}
	if got := res.Skipped["Fotos/anna"]; got != samba.SkipCaseClash || section(res.Files[confPath], "Fotos") == nil {
		t.Errorf("skipped = %v", res.Skipped)
	}
}

// No share or user name can make two include files collide, and no name is in the file name.
func TestIncludePathCollisionFree(t *testing.T) {
	if samba.IncludePath("a-b", "c") == samba.IncludePath("a", "b-c") {
		t.Error("share a-b with c and share a with b-c collide")
	}
	if samba.IncludePath("Fotos", "anna") != samba.IncludePath("fotos", "ANNA") {
		t.Error("the file name depends on case, but smbd's lookup does not")
	}
	if p := samba.IncludePath("My Share", "anna"); strings.ContainsAny(p, " ") || strings.Contains(p, "Share") {
		t.Errorf("IncludePath = %q", p)
	}
}

func TestRenderSanitizesComment(t *testing.T) {
	c := baseConfig()
	c.Shares = c.Shares[:1]
	c.Shares[0].Comment = "Bilder\n[evil]\r\npath = /etc\\"
	conf := render(t, c, baseEnv())[confPath]
	if section(conf, "evil") != nil || strings.Count(conf, "\npath = ") != 0 {
		t.Errorf("the comment broke out of its line:\n%s", conf)
	}
	if !hasLine(section(conf, "Fotos"), "comment = Bilder [evil] path = /etc") {
		t.Errorf("comment:\n%s", strings.Join(section(conf, "Fotos"), "\n"))
	}
	c.Shares[0].Comment = "100% sure"
	if sec := section(render(t, c, baseEnv())[confPath], "Fotos"); !hasLine(sec, "comment = 100 sure") {
		t.Errorf("a macro in the comment: %v", sec)
	}
}

func TestRenderPlainShare(t *testing.T) {
	sec := section(render(t, baseConfig(), baseEnv())[confPath], "Archiv")
	for _, l := range []string{
		"path = /srv/archiv", "browseable = no", "force user = myUser", "force group = myUser",
		"valid users = anna myUser", "read list = anna", "write list = myUser",
		"root preexec = '" + binary + "' --samba-tool check-share --name '%S' --client '%I'", "root preexec close = yes",
		"vfs objects = catia fruit streams_xattr",
	} {
		if !hasLine(sec, l) {
			t.Errorf("[Archiv] lacks %q:\n%s", l, strings.Join(sec, "\n"))
		}
	}
	for _, k := range []string{"inherit owner", "create mask", "directory mask", "hide unreadable", "acl_xattr:ignore system acls", "recycle:touch", "include", "recycle:repository"} {
		if hasKey(sec, k) {
			t.Errorf("[Archiv] has %q", k)
		}
	}
	if sec := section(render(t, baseConfig(), baseEnv())[confPath], "Fotos"); !hasLine(sec, "browseable = yes") {
		t.Errorf("a share that is not hidden is not browseable:\n%s", strings.Join(sec, "\n"))
	}
}

func TestRenderBinaryQuoted(t *testing.T) {
	env := baseEnv()
	env.Binary = "/opt/my dir/edelweiss-ext-samba"
	sec := section(render(t, baseConfig(), env)[confPath], "Fotos")
	if !hasLine(sec, `root preexec = '/opt/my dir/edelweiss-ext-samba' --samba-tool check-share --name '%S' --client '%I'`) {
		t.Errorf("preexec:\n%s", strings.Join(sec, "\n"))
	}
}

func TestRenderPerUser(t *testing.T) {
	conf := render(t, baseConfig(), baseEnv())[confPath]
	sec := section(conf, "Dokumente")
	for _, l := range []string{
		"force group = familie", "inherit owner = unix only", "create mask = 0664", "directory mask = 2775",
		"acl_xattr:ignore system acls = yes", "hide unreadable = yes",
		"vfs objects = catia fruit streams_xattr acl_xattr recycle",
		"valid users = anna ben myUser", "read list = ben", "write list = anna myUser",
	} {
		if !hasLine(sec, l) {
			t.Errorf("[Dokumente] lacks %q:\n%s", l, strings.Join(sec, "\n"))
		}
	}
	if hasKey(sec, "force user") {
		t.Errorf("[Dokumente] has force user:\n%s", strings.Join(sec, "\n"))
	}
	// Shares that are not converted keep the default masks.
	for _, n := range []string{"Fotos", "Archiv"} {
		s := section(conf, n)
		if hasKey(s, "create mask") || hasKey(s, "directory mask") || hasKey(s, "inherit owner") || hasKey(s, "hide unreadable") {
			t.Errorf("[%s] has folder-rights lines:\n%s", n, strings.Join(s, "\n"))
		}
	}
}

// In a share that runs per user, vfs_recycle makes #recycle and its folders as the user who deletes: 0700 would keep every
// other user out, and vfs_recycle then removes the files of those users for good instead of keeping them
// (testdata/README.md, "Per-user shares"). The group of the share may write them; a plain share keeps the default.
func TestRenderPerUserRecycle(t *testing.T) {
	conf := render(t, baseConfig(), baseEnv())[confPath]
	doc := section(conf, "Dokumente")
	for _, l := range []string{"recycle:directory_mode = 0770", "recycle:subdir_mode = 0770"} {
		if !hasLine(doc, l) {
			t.Errorf("[Dokumente] lacks %q:\n%s", l, strings.Join(doc, "\n"))
		}
	}
	if fotos := section(conf, "Fotos"); hasKey(fotos, "recycle:directory_mode") || hasKey(fotos, "recycle:subdir_mode") {
		t.Errorf("[Fotos] runs as the owner and needs no other modes:\n%s", strings.Join(fotos, "\n"))
	}
}

func TestRenderRecycleReaders(t *testing.T) {
	files := render(t, baseConfig(), baseEnv())
	conf := files[confPath]
	sec := section(conf, "Fotos")
	for _, l := range []string{
		"vfs objects = catia fruit streams_xattr recycle",
		"recycle:touch = yes", "recycle:repository = #recycle", "recycle:keeptree = yes", "recycle:versions = yes",
		"include = " + strings.TrimSuffix(samba.IncludePath("Fotos", "x"), "x.conf") + "%U.conf",
	} {
		if !hasLine(sec, l) {
			t.Errorf("[Fotos] lacks %q:\n%s", l, strings.Join(sec, "\n"))
		}
	}
	// Readers get the file, the writer does not (and so sees #recycle).
	for _, f := range []string{samba.IncludePath("Fotos", "anna"), samba.IncludePath("Fotos", "ben")} {
		if files[f] != "veto files = /#recycle/\n" {
			t.Errorf("%s = %q", f, files[f])
		}
	}
	if _, ok := files[samba.IncludePath("Fotos", "myUser")]; ok {
		t.Error("the writer has an include file")
	}
	// Without a recycle bin: no include line, no file.
	if hasKey(section(conf, "Archiv"), "include") {
		t.Error("[Archiv] includes a file")
	}
	for p := range files {
		if strings.Contains(p, "Archiv") {
			t.Errorf("file for a share without a recycle bin: %s", p)
		}
	}
	// A share with a space in its name has a file name without it.
	c := baseConfig()
	c.Shares = []samba.Share{{Name: "My Share", Path: "/srv/my", Recycle: true, Rights: map[string]samba.Right{"anna": samba.Read, "myUser": samba.Write}}}
	env := baseEnv()
	env.Owners["My Share"] = samba.Owner{User: "myUser", Group: "myUser"}
	files = render(t, c, env)
	prefix := strings.TrimSuffix(samba.IncludePath("My Share", "x"), "x.conf")
	if _, ok := files[samba.IncludePath("My Share", "anna")]; !ok || !hasLine(section(files[confPath], "My Share"), "include = "+prefix+"%U.conf") {
		t.Errorf("space in a name: %v\n%s", files, files[confPath])
	}
}

func TestRenderNoXattr(t *testing.T) {
	env := baseEnv()
	env.NoXattr = map[string]bool{"Fotos": true, "Dokumente": true}
	conf := render(t, baseConfig(), env)[confPath]
	fotos := section(conf, "Fotos")
	if !hasLine(fotos, "vfs objects = catia recycle") {
		t.Errorf("[Fotos]:\n%s", strings.Join(fotos, "\n"))
	}
	doc := section(conf, "Dokumente")
	if !hasLine(doc, "vfs objects = catia recycle") || hasKey(doc, "acl_xattr:ignore system acls") {
		t.Errorf("[Dokumente]:\n%s", strings.Join(doc, "\n"))
	}
	for _, s := range [][]string{fotos, doc} {
		for _, l := range s {
			if strings.Contains(l, "fruit") || strings.Contains(l, "streams_xattr") || strings.Contains(l, "acl_xattr") {
				t.Errorf("an xattr module is left: %s", l)
			}
		}
	}
	// The other share keeps them.
	if !hasLine(section(conf, "Archiv"), "vfs objects = catia fruit streams_xattr") {
		t.Error("[Archiv] lost its modules")
	}
}

// A plain share must not load acl_xattr: an ACL edit from a client would write security.NTACL on existing files.
func TestRenderPlainShareHasNoACLModule(t *testing.T) {
	conf := render(t, baseConfig(), baseEnv())[confPath]
	for _, n := range []string{"Fotos", "Archiv"} {
		for _, l := range section(conf, n) {
			if strings.Contains(l, "acl_xattr") {
				t.Errorf("[%s] has %s", n, l)
			}
		}
	}
}
