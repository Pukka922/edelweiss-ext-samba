//go:build integration

package integration

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/tool"
)

// The per-user share of TestACLAgainstSamba, and a folder outside it that links in the share point to.
const (
	puName    = "pu"
	puRoot    = "/srv/pu"
	puOutside = "/srv/outside"
)

// The rights of the share and of its folder sub: usera may write in both, userb may write in the share and read in sub,
// userc may read in the share and has nothing in sub.
var (
	puShareRights = map[string]samba.Right{"usera": samba.Write, "userb": samba.Write, "userc": samba.Read}
	puSubRights   = map[string]samba.Right{"usera": samba.Write, "userb": samba.Read, "userc": ""}
)

// TestACLAgainstSamba is the folder rights of the extension against the real smbd, on a share that is converted to run
// per user. It needs SYS_ADMIN: only that capability may write security.NTACL. The share's folder holds data of before
// (#recycle with a deleted file among it, and links to a folder outside); --samba-tool convert gives the group write,
// apply-acl puts the share's rights on the whole tree, the config of the share runs per user (samba.Render), and apply-acl
// puts the narrowed rights on its folder sub. An NT owner's DACL-only change is tried before and after the share gets its
// share ACL (sharesec, as the setup sets it). Then usera, userb and userc work in the share through smbclient: what they
// may, what is refused and hidden, who owns what they make and with which modes, the recycle bin and the folders it
// makes, restores (of a folder that is gone, and of a file from before the share ran per user), and an apply of the
// share's rights again that keeps sub. The SIDs come from pdbedit -L -v, as the plan of a job takes them.
func TestACLAgainstSamba(t *testing.T) {
	c := newCapture(t)
	block("pu-accounts", "addgroup -S "+marker+": "+c.markerGroup()+"adduser "+owner+": "+c.hostUser())
	for _, u := range aclUsers {
		c.sambaUser(u[0], u[1])
	}
	users, err := samba.Tools{R: exe, T: host.FindTools("/")}.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sids := map[string]string{}
	for _, u := range users {
		sids[u.Name] = u.SID
	}
	c.puTree()
	fsid, err := paths.FsidOf(host.FS{Root: "/"}, "/")
	if err != nil {
		t.Fatal(err)
	}
	cfg := samba.Config{Shares: []samba.Share{{Name: puName, Path: puRoot, Mount: "/", Fsid: fsid, Rights: puShareRights,
		Recycle: true, RecycleDays: 30}}}
	writeFile(t, samba.ConfigPath, string(cfg.JSON()))

	c.puConvert()
	args, err := acl.Plan{Share: puName, Rights: puShareRights}.Args(sids)
	if err != nil {
		t.Fatal(err)
	}
	block("pu-apply-share", c.samToolStep(args...))
	cfg.Shares[0].PerUser = true
	cfg.Shares[0].Folders = map[string]map[string]samba.Right{"sub": puSubRights}
	writeFile(t, samba.ConfigPath, string(cfg.JSON()))
	conf := c.puRender(cfg)
	block("pu-start", c.startSamba())
	narrowed := map[string]samba.Right{}
	for u, r := range puShareRights {
		narrowed[u] = samba.Narrow(r, puSubRights[u])
	}
	if args, err = (acl.Plan{Share: puName, Folder: "sub", Rights: narrowed}).Args(sids); err != nil {
		t.Fatal(err)
	}
	block("pu-apply-sub", c.samToolStep(args...))
	block("pu-readback", c.puReadback("", "sub", "sub/old.txt", "sub/deep", "top.txt", "#recycle"))
	subRights := map[string]samba.Right{sids["usera"]: samba.Write, sids["userb"]: samba.Read}
	c.puRead("", map[string]samba.Right{sids["usera"]: samba.Write, sids["userb"]: samba.Write, sids["userc"]: samba.Read})
	c.puRead("sub", subRights)
	if t.Failed() {
		c.finish()
		return
	}

	// an NT owner's change of only the DACL, before the share has its share ACL; then the share ACL, as the setup sets it
	writeFile(t, "/tmp/local.txt", "local\n")
	worked, text := c.puDACL(`sub\a-own1.txt`, sids)
	block("pu-dacl-without-share-acl", text+"the DACL-only change worked: "+yesNo(worked)+"\n")
	if err := os.Remove(puRoot + "/sub/a-own1.txt"); err != nil { // its ACL may now differ from what sub gives
		t.Fatal(err)
	}
	cmd, err := samba.ShareACLCmd(host.FindTools("/"), puName, puShareRights, sids)
	if err != nil {
		t.Fatal(err)
	}
	block("pu-share-acl", c.result(cmd.Path, cmd.Args...)+c.result(c.tool("sharesec"), puName, "--view"))

	c.puEnforce()
	c.puWriteDAC(sids)
	c.puRecycle()
	c.puRestore(subRights)
	shareRead := map[string]samba.Right{sids["usera"]: samba.Write, sids["userb"]: samba.Write, sids["userc"]: samba.Read}
	c.puRead("", shareRead)
	c.puRead("sub", subRights)
	// the share's rights again, as a job applies them: sub, which has rights of its own, is kept
	if args, err = (acl.Plan{Share: puName, Rights: puShareRights, Keep: []string{"sub"}}).Args(sids); err != nil {
		t.Fatal(err)
	}
	block("pu-apply-share-keep", c.samToolStep(args...))
	c.puRead("", shareRead)
	c.puRead("sub", subRights)
	c.puModeVariants(conf, sids)
	c.finish()
}

// samToolStep runs a subcommand of --samba-tool in this process, on the host, as the step of a job runs the binary; it
// has to end with 0. It returns what it printed, below the command and its exit status.
func (c *capture) samToolStep(args ...string) string {
	c.t.Helper()
	var out, errb bytes.Buffer
	status := tool.Tool{FS: host.FS{Root: "/"}, Stdout: &out, Stderr: &errb}.Run(ctx, args)
	if status != 0 {
		c.t.Errorf("--samba-tool %s: exit status %d\n%s%s", strings.Join(args, " "), status, &out, &errb)
	}
	return fmt.Sprintf("--samba-tool %s\nexit status: %d\n%s%s", strings.Join(args, " "), status, &out, &errb)
}

// puTree makes the folder of the share with data that was there before, all of the owner: makeTree's files with old
// times, a folder of 0700 with a file of 0600, #recycle as smbd makes it under force user (0700) with a deleted file in
// a folder, and links to a folder outside the share.
func (c *capture) puTree() {
	c.t.Helper()
	c.makeTree(puRoot)
	c.folder(puRoot + "/private")
	c.oldFile(puRoot+"/private/p.txt", "p\n")
	c.folder(puRoot + "/#recycle")
	c.folder(puRoot + "/#recycle/dir")
	c.oldFile(puRoot+"/#recycle/dir/gone.txt", "gone\n")
	c.folder(puRoot + "/#recycle/sub")
	c.oldFile(puRoot+"/#recycle/sub/gone.txt", "gone from sub\n")
	c.sh("chmod 0700 '" + puRoot + "/#recycle/sub'")
	c.folder(puOutside)
	c.oldFile(puOutside+"/secret.txt", "secret\n")
	c.sh("chmod 0700 '" + puRoot + "/private' '" + puRoot + "/#recycle' '" + puRoot + "/#recycle/dir' " + puOutside +
		" && chmod 0600 '" + puRoot + "/private/p.txt' " + puOutside + "/secret.txt && ln -s " + puOutside + " '" +
		puRoot + "/outside-link' && ln -s " + puOutside + "/secret.txt '" + puRoot + "/sub/secret-link'")
}

// puConvert runs convert and checks what it did: files gained g+rw, folders g+rwx and the default ACL (the folders of
// #recycle too), and nothing else changed; the files of #recycle, the links and what they point to kept everything, the
// change time too.
func (c *capture) puConvert() {
	c.t.Helper()
	before, outside := c.manifest(puRoot), c.manifest(puOutside)
	block("pu-stats-before", c.treeStats(puRoot))
	block("pu-convert", c.samToolStep("convert", "--share", puName))
	block("pu-stats-converted", c.treeStats(puRoot))
	after := c.manifest(puRoot)
	c.compare("convert", before, after, nil, func(p string, e entry) delta {
		switch {
		case e.mode&unix.S_IFMT == unix.S_IFDIR:
			return delta{modeAdd: 0o070, ctime: true, attrs: []string{acl.DefaultACLAttr}}
		case e.mode&unix.S_IFMT == unix.S_IFREG && !strings.HasPrefix(p, puRoot+"/#recycle/"):
			return delta{modeAdd: 0o060, ctime: true}
		}
		return delta{}
	})
	if !maps.Equal(c.manifest(puOutside), outside) {
		c.t.Error("convert followed a link out of the share")
	}
	// every folder (all are the owner's group here), those of #recycle too, has the default ACL; no file has one
	text := ""
	for p, e := range after {
		got := defaultACLOf(p)
		if e.mode&unix.S_IFMT == unix.S_IFDIR {
			text += p + ": " + acl.DefaultACLAttr + " = " + hex.EncodeToString(got) + "\n"
		}
		if want := e.mode&unix.S_IFMT == unix.S_IFDIR; want != bytes.Equal(got, acl.DefaultACL()) || !want && got != nil {
			c.t.Errorf("convert: %s: default ACL %x", p, got)
		}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	slices.Sort(lines)
	block("pu-default-acls", strings.Join(lines, "\n")+"\n")
}

// defaultACLOf is the POSIX default ACL of the path, nil when it has none (a file, a link, or a folder without one).
func defaultACLOf(p string) []byte {
	buf := make([]byte, 4096)
	n, err := unix.Lgetxattr(p, acl.DefaultACLAttr, buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// wantMode checks the mode of the path, and for a folder that it has the default ACL of the conversion.
func (c *capture) wantMode(p string, mode uint32) {
	c.t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil || st.Mode&0o7777 != mode {
		c.t.Errorf("%s: mode %04o, want %04o (%v)", p, st.Mode&0o7777, mode, err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR && !bytes.Equal(defaultACLOf(p), acl.DefaultACL()) {
		c.t.Errorf("%s: default ACL %x, want the conversion's", p, defaultACLOf(p))
	}
}

// puRender renders the config as the extension does (the root preexec is /bin/true: check-share has tests of its own),
// writes its files, and checks smb.conf with testparm; it returns smb.conf.
func (c *capture) puRender(cfg samba.Config) string {
	c.t.Helper()
	res, err := samba.Render(cfg, samba.RenderEnv{Owners: map[string]samba.Owner{puName: {User: owner, Group: owner}},
		SambaUsers: map[string]bool{"usera": true, "userb": true, "userc": true}, Binary: "/bin/true"})
	if err != nil {
		c.t.Fatal(err)
	}
	if len(res.Skipped) > 0 {
		c.t.Fatalf("Render skipped %v", res.Skipped)
	}
	text := ""
	for _, p := range slices.Sorted(maps.Keys(res.Files)) {
		writeFile(c.t, p, res.Files[p])
		text += "--- " + p + "\n" + res.Files[p]
	}
	block("pu-render", text)
	if out := c.step(c.tool("testparm"), "-s", smbConf); strings.Contains(out, "nknown parameter") {
		c.t.Errorf("testparm -s: a parameter that Samba does not know\n%s", out)
	}
	return res.Files[smbConf]
}

// puReadback is smbcacls --numeric as usera (who may read the ACLs) of the paths below the share, and their
// attributes on disk.
func (c *capture) puReadback(paths ...string) string {
	c.t.Helper()
	text := ""
	for _, p := range paths {
		win := strings.ReplaceAll(p, "/", `\`)
		if win == "" {
			win = `\`
		}
		text += "--- smbcacls " + win + "\n" + c.result(c.tool("smbcacls"), "//127.0.0.1/"+puName, win, "-U", "usera%"+pwA,
			"--numeric")
	}
	for _, p := range paths {
		text += xattrs(filepath.Join(puRoot, p))
	}
	return text
}

// puRead checks what acl.Read says of the folder: the rights, by SID, and no mixed state.
func (c *capture) puRead(folder string, want map[string]samba.Right) {
	c.t.Helper()
	root, err := safefs.Open("/")
	if err != nil {
		c.t.Fatal(err)
	}
	defer root.Close()
	rights, mixed, partial, err := acl.Read(root, puRoot, folder)
	if err != nil || mixed || partial || !maps.Equal(rights, want) {
		c.t.Errorf("Read(%q) = %v, mixed %v, partial %v, %v; want %v", folder, rights, mixed, partial, err, want)
	}
}

// try runs smbclient on the share as the user and says whether it worked: exit status 0 and no NT_STATUS_ in the output.
func (c *capture) try(share, user, pw, cmds string) (bool, string) {
	c.t.Helper()
	out, status := c.run(c.tool("smbclient"), "//127.0.0.1/"+share, "-U", user+"%"+pw, "-c", cmds)
	ok := status == 0 && !strings.Contains(out, "NT_STATUS_")
	return ok, fmt.Sprintf("--- %s: %s as %s: %s\n%s", share, cmds, user, yesNo(ok), withStatus(out, status))
}

var recycleListed = regexp.MustCompile(`(?m)^\s+#recycle\s+D`)

func pwOf(user string) string {
	for _, u := range aclUsers {
		if u[0] == user {
			return u[1]
		}
	}
	return ""
}

// modeLine is the owner, group and mode of the path, and whether others may write it.
func modeLine(p string) string {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return fmt.Sprintf("%s: %v\n", p, err)
	}
	return fmt.Sprintf("%s uid=%d gid=%d mode=%04o world-writable=%s\n", p, st.Uid, st.Gid, st.Mode&0o7777,
		yesNo(st.Mode&0o002 != 0))
}

// puEnforce works in the share as usera, userb and userc and checks each result: the share's rights in the share, the
// narrowed ones in sub; userc does not see sub, and not #recycle. What usera and userb make belongs to the owner and the
// group of the share's folder; its modes are recorded.
func (c *capture) puEnforce() {
	c.t.Helper()
	writeFile(c.t, "/tmp/local.txt", "local\n")
	text := ""
	for _, op := range []struct {
		user, cmds string
		want       bool
	}{
		{"usera", "ls", true},
		{"usera", `get top.txt /tmp/pu.txt`, true},
		{"usera", `put /tmp/local.txt a-root.txt; mkdir a-rootdir`, true},
		{"usera", `ls sub\*`, true},
		{"usera", `get sub\old.txt /tmp/pu.txt`, true},
		{"usera", `put /tmp/local.txt sub\a-sub.txt; mkdir sub\a-dir; put /tmp/local.txt sub\a-dir\inner.txt`, true},
		{"usera", `put /tmp/local.txt sub\old.txt`, true},
		{"userb", `put /tmp/local.txt b-root.txt; mkdir b-rootdir`, true},
		{"userb", `ls sub\*`, true},
		{"userb", `get sub\old.txt /tmp/pu.txt`, true},
		{"userb", `put /tmp/local.txt sub\b-sub.txt`, false},
		{"userb", `mkdir sub\b-dir`, false},
		{"userb", `put /tmp/local.txt sub\deep\old2.txt`, false},
		{"userb", `del sub\old.txt`, false},
		{"userc", "ls", true},
		{"userc", `get top.txt /tmp/pu.txt`, true},
		{"userc", `put /tmp/local.txt c-root.txt`, false},
		{"userc", `ls sub\*`, false},
		{"userc", `get sub\old.txt /tmp/pu.txt`, false},
		{"userc", `ls #recycle\*`, false},
	} {
		ok, out := c.try(puName, op.user, pwOf(op.user), op.cmds)
		text += out
		if ok != op.want {
			c.t.Errorf("%s as %s: worked %v, want %v\n%s", op.cmds, op.user, ok, op.want, out)
		}
		if op.cmds == "ls" {
			if seesSub := subListed.MatchString(out); seesSub != (op.user != "userc") {
				c.t.Errorf("%s sees sub in the share: %v", op.user, seesSub)
			}
			if op.user == "userc" && recycleListed.MatchString(out) {
				c.t.Errorf("userc sees #recycle")
			}
		}
	}
	block("pu-enforce", text)
	text = ""
	for _, p := range []string{"a-root.txt", "a-rootdir", "b-root.txt", "b-rootdir", "sub/a-sub.txt", "sub/a-dir",
		"sub/a-dir/inner.txt"} {
		full := filepath.Join(puRoot, p)
		text += modeLine(full)
		var st unix.Stat_t
		if err := unix.Lstat(full, &st); err != nil || st.Uid != 1000 || st.Gid != 1000 {
			c.t.Errorf("%s: owner %d:%d (%v), want the share folder's 1000:1000", p, st.Uid, st.Gid, err)
		}
		// the default ACL of the folder caps smbd's 0666 and 0777; a new folder has it itself
		mode := uint32(0o664)
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			mode = 0o775
			text += full + ": " + acl.DefaultACLAttr + " = " + hex.EncodeToString(defaultACLOf(full)) + "\n"
		}
		c.wantMode(full, mode)
	}
	block("pu-new-modes", text)
}

// puDACL makes the file below the share as usera and then tries, as usera, to change only its DACL: smbcacls --set
// sends no owner and so opens the file with WRITE_DAC alone, as the Sicherheit tab of Windows does. usera is the NT owner
// of the file (inherit owner = unix only), which gets WRITE_DAC without an ACE. It says whether the change worked.
func (c *capture) puDACL(file string, sids map[string]string) (bool, string) {
	c.t.Helper()
	text := ""
	if file != `sub\old.txt` {
		_, text = c.try(puName, "usera", pwA, `put /tmp/local.txt `+file)
	}
	out, status := c.run(c.tool("smbcacls"), "//127.0.0.1/"+puName, file, "-U", "usera%"+pwA, "--numeric", "--set",
		"ACL:"+sids["usera"]+":ALLOWED/0/FULL")
	text += "--- usera sets the DACL of " + file + " to usera FULL (--set: WRITE_DAC only)\n" + withStatus(out, status) +
		c.result(c.tool("smbcacls"), "//127.0.0.1/"+puName, file, "-U", "usera%"+pwA, "--numeric")
	return status == 0 && !strings.Contains(out, "NT_STATUS_"), text
}

// puWriteDAC checks that with the share ACL (sharesec READ/CHANGE, never FULL) usera, who may change files, may not
// change their DACL: not of a file usera made and owns in the NT ACL, and not of a file that was there. The share ACL
// caps WRITE_DAC for everyone who connects, NT owners included.
func (c *capture) puWriteDAC(sids map[string]string) {
	c.t.Helper()
	text := ""
	for _, p := range []string{`sub\a-own2.txt`, `sub\old.txt`} {
		worked, out := c.puDACL(p, sids)
		text += out
		if worked || !strings.Contains(out, "NT_STATUS_ACCESS_DENIED") {
			c.t.Errorf("usera changed the DACL of %s with the share ACL set:\n%s", p, out)
		}
	}
	block("pu-dacl-with-share-acl", text)
}

// puRecycle deletes as usera and userb, in the share's folder and in sub, and checks that every deleted file is in
// #recycle, the one #recycle that was there before the share ran per user included. A folder that vfs_recycle makes
// (#recycle/sub/a-dir, for usera's deletion of sub\a-dir\inner.txt) belongs to the user who deleted, in the share's
// group, 0770, and has no security.NTACL: vfs_recycle sits below acl_xattr and makes it without one.
func (c *capture) puRecycle() {
	c.t.Helper()
	_, a := c.try(puName, "usera", pwA, `del top.txt; del sub\a-sub.txt; del sub\a-dir\inner.txt`)
	_, b := c.try(puName, "userb", pwB, `del b-root.txt; del a-root.txt`)
	made := puRoot + "/#recycle/sub/a-dir"
	text := a + b + c.sh("ls -laR '"+puRoot+"/#recycle'") + c.sh("stat -c '%n %U:%G %a' '"+made+"'") + xattrs(made) +
		"--- smbcacls #recycle\\sub\\a-dir as usera\n" +
		c.result(c.tool("smbcacls"), "//127.0.0.1/"+puName, `#recycle\sub\a-dir`, "-U", "usera%"+pwA, "--numeric")
	block("pu-recycle", text)
	for _, p := range []string{"top.txt", "sub/a-sub.txt", "sub/a-dir/inner.txt", "b-root.txt", "a-root.txt"} {
		if !exists(filepath.Join(puRoot, "#recycle", p)) || exists(filepath.Join(puRoot, p)) {
			c.t.Errorf("%s is not in the recycle bin", p)
		}
	}
	// the group of the share's folder is the owner's group of the same name
	if got := strings.TrimSpace(c.sh("stat -c '%U:%G %a' '" + made + "'")); got != "usera:"+owner+" 770" {
		c.t.Errorf("the folder vfs_recycle made: %s, want usera:%s 770", got, owner)
	}
	if _, err := unix.Lgetxattr(made, "security.NTACL", nil); !errors.Is(err, unix.ENODATA) {
		c.t.Errorf("the folder vfs_recycle made has security.NTACL, or it cannot be read: %v", err)
	}
}

// puRestore deletes sub/deep/old2.txt and then the folder sub/deep as usera, restores the file with --samba-tool
// recycle-restore, and checks that the folder it made again has the rights it inherits from sub: usera reads the file
// there, userc may not list it.
func (c *capture) puRestore(subRights map[string]samba.Right) {
	c.t.Helper()
	_, out := c.try(puName, "usera", pwA, `del sub\deep\old2.txt; rmdir sub\deep`)
	text := out
	if exists(puRoot + "/sub/deep") {
		c.t.Fatalf("sub/deep is still there:\n%s", out)
	}
	text += c.samToolStep("recycle-restore", "--share", puName, "--item", "sub/deep/old2.txt") + modeLine(puRoot+"/sub/deep") +
		c.puReadback("sub/deep", "sub/deep/old2.txt")
	c.puRead("sub/deep", subRights)
	c.wantMode(puRoot+"/sub/deep", 0o775)
	c.wantMode(puRoot+"/sub/deep/old2.txt", 0o664)
	ok, out := c.try(puName, "usera", pwA, `get sub\deep\old2.txt /tmp/pu.txt`)
	text += out
	if !ok {
		c.t.Error("usera cannot read the restored file")
	}
	ok, out = c.try(puName, "userc", pwC, `ls sub\deep\*`)
	text += out
	if ok {
		c.t.Error("userc lists the folder that restore made")
	}

	// a file deleted from sub before the share ran per user has no security.NTACL and its old mode 0644; restored, it has
	// the rights of sub and the group bits of the conversion
	text += modeLine(puRoot+"/#recycle/sub/gone.txt") + xattrs(puRoot+"/#recycle/sub/gone.txt") +
		c.samToolStep("recycle-restore", "--share", puName, "--item", "sub/gone.txt") + modeLine(puRoot+"/sub/gone.txt") +
		c.puReadback("sub/gone.txt")
	c.wantMode(puRoot+"/sub/gone.txt", 0o664)
	for _, try := range []struct {
		user string
		want bool
	}{{"usera", true}, {"userb", true}, {"userc", false}} {
		ok, out := c.try(puName, try.user, pwOf(try.user), `get sub\gone.txt /tmp/pu.txt`)
		text += out
		if ok != try.want {
			c.t.Errorf("%s reads the restored sub/gone.txt: %v, want %v", try.user, ok, try.want)
		}
	}
	block("pu-restore", text)
}

// puModeVariants records, in copies of the share (each a converted folder with the share's rights), the modes of what
// usera makes with inherit permissions = yes, with a POSIX default ACL (u::rwx,g::rwx,o::r-x) on the folder, and without
// acl_xattr:ignore system acls (against decision a); and the recycle bin without the recycle modes of the renderer. acl_xattr with ignore system acls sets create mask 0666,
// directory mask 0777 and inherit acls, and smbd then makes files 0666 and folders 0777, whatever the masks of the share
// say; only a default ACL limits that. Without recycle:directory_mode, #recycle is 0700 of the first user who deletes,
// and what another user deletes is gone (testdata README, "Per-user shares").
func (c *capture) puModeVariants(conf string, sids map[string]string) {
	c.t.Helper()
	root, err := safefs.Open("/")
	if err != nil {
		c.t.Fatal(err)
	}
	defer root.Close()
	grants := map[string]acl.Grant{}
	for u, r := range puShareRights {
		grants[u] = acl.Grant{Right: r, SID: sids[u]}
	}
	text := c.result(c.tool("apk"), "add", "--quiet", "acl")
	secs := conf
	for _, v := range []struct{ name, extra string }{{"pu-ip", "inherit permissions = yes"}, {"pu-dacl", ""}, {"pu-nosys", ""},
		{"pu-rb0", ""}} {
		dir := "/srv/" + v.name
		c.folder(dir)
		c.oldFile(dir+"/one.txt", "one\n")
		c.oldFile(dir+"/two.txt", "two\n")
		if _, err := acl.Convert(ctx, root, dir, nil); err != nil {
			c.t.Fatal(err)
		}
		if _, err := acl.Apply(ctx, root, dir, "", grants, nil, nil); err != nil {
			c.t.Fatal(err)
		}
		// the variants show smb.conf without the default ACL that convert writes (pu-dacl gets one from setfacl)
		if err := unix.Lremovexattr(dir, "system.posix_acl_default"); err != nil && !errors.Is(err, unix.ENODATA) {
			c.t.Fatal(err)
		}
		sec := conf[strings.Index(conf, "\n["+puName+"]\n")+1:]
		sec = strings.Replace(strings.Replace(sec, "["+puName+"]", "["+v.name+"]", 1), "path = "+puRoot+"\n", "path = "+dir+"\n", 1)
		switch v.name {
		case "pu-rb0":
			sec = strings.Replace(strings.Replace(sec, "\trecycle:directory_mode = 0770\n", "", 1), "\trecycle:subdir_mode = 0770\n", "", 1)
		case "pu-nosys": // against decision a: acl_xattr maps the NT ACL to POSIX ACLs
			sec = strings.Replace(sec, "\tacl_xattr:ignore system acls = yes\n", "", 1)
		}
		if v.extra != "" {
			sec += "\t" + v.extra + "\n"
		}
		secs += "\n" + sec
	}
	text += c.sh("setfacl -d -m u::rwx,g::rwx,o::rx /srv/pu-dacl && getfacl /srv/pu-dacl 2>/dev/null") + xattrs("/srv/pu-dacl")
	writeFile(c.t, smbConf, secs)
	c.step(c.tool("testparm"), "-s", smbConf)
	for _, v := range []string{"pu-ip", "pu-dacl", "pu-nosys"} {
		dir := "/srv/" + v
		_, out := c.try(v, "usera", pwA, `put /tmp/local.txt a-new.txt; mkdir a-dir; put /tmp/local.txt a-dir\inner.txt`)
		text += out + modeLine(dir+"/a-new.txt") + modeLine(dir+"/a-dir") + modeLine(dir+"/a-dir/inner.txt")
	}
	text += c.sh("getfacl /srv/pu-dacl/a-dir /srv/pu-nosys/a-new.txt /srv/pu-nosys/a-dir 2>/dev/null")
	block("pu-mode-variants", text)
	_, a := c.try("pu-rb0", "usera", pwA, "del one.txt")
	_, b := c.try("pu-rb0", "userb", pwB, "del two.txt")
	block("pu-recycle-without-modes", a+b+c.sh("ls -la /srv/pu-rb0; ls -laR '/srv/pu-rb0/#recycle'"))
}
