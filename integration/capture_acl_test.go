//go:build integration

package integration

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
)

// The users of the folder-rights capture: on the share all three may write; on its folder sub usera may write, userb
// may read, and userc may do nothing.
const (
	pwA = "pw-usera"
	pwB = "pw-userb"
	pwC = "pw-userc"
	// aclAccount is the hidden internal Samba account of method (b): admin users makes smbd work as root for it, in a
	// hidden share of /srv. Its password is in the authentication file aclAuth.
	aclAccount  = "edelweiss-acl"
	aclPassword = "pw-internal"
	aclAuth     = "/root/edelweiss-acl.auth"
	aclAdmin    = "edelweiss-acl$"
)

var aclUsers = [][2]string{{"usera", pwA}, {"userb", pwB}, {"userc", pwC}}

// maskFull is the access mask smbcacls calls FULL, which only the probes give (the folder rights never do); READ and
// CHANGE are acl.MaskRead and acl.MaskChange. oici are the ACE flags object and container inherit.
const (
	maskFull = 0x001f01ff
	oici     = acl.FlagObjectInherit | acl.FlagContainerInherit
)

const (
	// ntaclName is the attribute where vfs_acl_xattr keeps the NT ACL of a file.
	ntaclName = "security.NTACL"
	// aclPreexecLog is where the root preexec of aclShare writes its arguments (preexecArgs).
	aclPreexecLog = "/tmp/preexec.log"
	// aclTree are the paths below the shared folder that the readback shows (makeTree makes them, top.txt aside).
	aclTree = "sub sub/old.txt sub/deep sub/deep/old2.txt"
)

// aclShare is the share name of the folder root with the folder rights of vfs_acl_xattr, as the extension would write
// it: smbd works as the owner of the folder, the share lets all three users write, and its root preexec writes the
// session user (%U) and the user smbd works as (%u) to aclPreexecLog.
func aclShare(name, root string, extra ...string) string {
	return ownedShare(name, root, append([]string{"valid users = usera userb userc", "write list = usera userb userc",
		"vfs objects = acl_xattr", "acl_xattr:ignore system acls = yes", "hide unreadable = yes",
		"root preexec = /tmp/preexec-args.sh %S session=%U forced=%u group=%g"}, extra...)...)
}

// adminShare is the hidden share of /srv for the internal account (method b).
var adminShare = section(aclAdmin, "path = /srv", "valid users = "+aclAccount, "admin users = "+aclAccount,
	"read only = no", "browseable = no", "vfs objects = acl_xattr", "acl_xattr:ignore system acls = yes")

// aclEnv holds the SIDs the ACLs name: the Samba users', and those smbd gives the owner of the folders.
type aclEnv struct {
	sid          map[string]string
	owner, group string
}

// TestCaptureACL is the gate of the folder rights. It needs SYS_ADMIN: the kernel lets only that capability write
// security.* attributes, where vfs_acl_xattr keeps the NT ACLs. It sets the rights of sub on a tree with existing files
// in three ways, (a) samba-tool ntacl set, (b) smbcacls over the loopback as a hidden internal account and (c)
// security.NTACL written from Go, reads each back, and checks after each as usera, userb and userc whether smbd
// enforces them per user while it works as the owner of the folder (force user). Then it shows whose token smbd checks,
// the same without force user, and an ACL on #recycle. The answer is the last line of the block acl-gate: "yes" or
// "no" only from a method whose ACL smbd reads back (storedACL), else "unknown", which fails the test.
func TestCaptureACL(t *testing.T) {
	c := newCapture(t)
	a := c.aclSetup()
	text := ""
	var outcomes []outcome
	for _, m := range []struct {
		name string
		set  func(aclEnv)
	}{{"a", c.aclSambaTool}, {"b", c.aclSmbcacls}, {"c", c.aclGo}} {
		m.set(a)
		if c.storedACL(a) {
			o := c.aclEnforce(m.name, "acl", "/srv/acl")
			outcomes = append(outcomes, o)
			text += fmt.Sprintf("method %s: smbd reads the ACL back; per-user NT ACLs under force user: %s; "+
				"usera, userb and userc got the same: %s\n", m.name, yesNo(o.perUser()), yesNo(o.alike()))
		} else {
			text += fmt.Sprintf("method %s: smbd reads back no ACL with usera's and userb's ACEs on sub\n", m.name)
		}
		block("acl-"+m.name+"-reset", c.resetACL("/srv/acl"))
	}
	gate := gateAnswer(outcomes)
	if gate == "unknown" {
		c.t.Errorf("gate unknown: no method left an ACL that smbd reads back with usera's and userb's ACEs on sub and that " +
			"gave the users either the rights per user or the same results (blocks acl-*-readback, acl-enforce-*)")
	}
	o := c.aclToken(a)
	control := o.alike() && o.ok["a-list-sub"] && o.ok["a-read"] && o.ok["a-write"]
	if !control {
		c.t.Errorf("the control failed: with an ACE of full rights for the folder owner %s on sub, usera, userb and userc "+
			"should all list, read and write there (block acl-enforce-owner-ace)", a.owner)
	}
	text += fmt.Sprintf("with an ACE of full rights for the folder owner %s: all three list, read and write: %s "+
		"(smbd checks the token of the user it works as, not the session user's)\n", a.owner, yesNo(control))
	o = c.aclWithoutForceUser(a)
	text += fmt.Sprintf("without force user (force group, inherit owner = unix only, a group-writable copy of the tree): "+
		"per-user: %s\n", yesNo(o.perUser()))
	c.aclRecycle(a)
	block("acl-gate", text+"Gate: per-user NT ACLs under force user: "+gate+"\n")
	block("acl-preexec-log", fileText(aclPreexecLog))
	block("acl-parameters", c.sh(`testparm -sv 2>/dev/null | grep -i -E '^[[:space:]]*[^=]*(force|inherit|acl)[^=]* = '; true`))
	c.finish()
}

// outcome holds what each user could do in sub: "<a|b|c>-<what>" → whether it worked.
type outcome struct{ ok map[string]bool }

// perUser says whether the rights of sub held per user: usera reads and writes, userb reads and cannot write, userc
// can do nothing there.
func (o outcome) perUser() bool {
	return o.ok["a-read"] && o.ok["a-write"] && o.ok["b-read"] && !o.ok["b-write"] && !o.ok["c-read"] && !o.ok["c-write"] &&
		!o.ok["c-list-sub"]
}

// alike says whether the three users could do exactly the same.
func (o outcome) alike() bool {
	for k, v := range o.ok {
		if o.ok["a"+k[1:]] != v {
			return false
		}
	}
	return true
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// makeTree makes the folder root with sub, sub/old.txt, sub/deep, sub/deep/old2.txt and top.txt, all of the owner,
// the files with old times.
func (c *capture) makeTree(root string) {
	c.t.Helper()
	c.folder(root)
	c.folder(root + "/sub")
	c.folder(root + "/sub/deep")
	c.oldFile(root+"/top.txt", "top\n")
	c.oldFile(root+"/sub/old.txt", "old\n")
	c.oldFile(root+"/sub/deep/old2.txt", "old2\n")
}

// treeStats lists owner, mode and mtime of everything below root.
func (c *capture) treeStats(root string) string {
	c.t.Helper()
	return c.sh("find '" + root + "' -exec stat -c '%n %U:%G %a mtime=%Y' {} + | sort")
}

// cacls is smbcacls --numeric of the path (below /srv, with backslashes) in the admin share as the internal account;
// the other arguments follow.
func (c *capture) cacls(path string, args ...string) string {
	c.t.Helper()
	return c.result(c.tool("smbcacls"), append([]string{"//127.0.0.1/" + aclAdmin, path, "-A", aclAuth, "--numeric"},
		args...)...)
}

// readback is what smbd says of the ACLs of the folder top below /srv and of its tree (smbcacls), and the attributes
// on disk.
func (c *capture) readback(top string) string {
	c.t.Helper()
	paths := append([]string{""}, strings.Fields(aclTree)...)
	text := ""
	for _, p := range paths {
		win := strings.ReplaceAll(strings.TrimSuffix(top+"/"+p, "/"), "/", `\`)
		text += "--- smbcacls " + win + "\n" + c.cacls(win)
	}
	for _, p := range paths {
		text += xattrs(filepath.Join("/srv", top, p))
	}
	return text
}

// sdField is the value of the first line of out that starts with key ("OWNER:").
func sdField(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, key); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (c *capture) aclSetup() aclEnv {
	c.t.Helper()
	writeFile(c.t, "/tmp/preexec-args.sh", preexecArgs)
	c.sh("chmod 0755 /tmp/preexec-args.sh")
	writeFile(c.t, "/tmp/local.txt", "local\n")
	block("acl-start", c.samba(globalConf(), aclShare("acl", "/srv/acl"), adminShare))
	c.makeTree("/srv/acl")
	for _, u := range aclUsers {
		c.sambaUser(u[0], u[1])
	}
	c.sambaUser(aclAccount, aclPassword)
	writeFile(c.t, aclAuth, "username = "+aclAccount+"\npassword = "+aclPassword+"\ndomain = CAPTURE\n")
	if err := os.Chmod(aclAuth, 0o600); err != nil {
		c.t.Fatal(err)
	}
	a := aclEnv{sid: c.samSIDs()}
	block("acl-users", c.result(c.tool("pdbedit"), "-L", "-v"))
	block("acl-xattr-privilege", xattrProbe("/srv/acl/xattr-probe"))
	if err := os.Remove("/srv/acl/xattr-probe"); err != nil {
		c.t.Fatal(err)
	}
	text := c.readback("acl")
	a.owner, a.group = sdField(text, "OWNER:"), sdField(text, "GROUP:")
	block("acl-synthesized", text)
	block("acl-stats-before", c.treeStats("/srv/acl"))
	if a.owner == "" || a.group == "" || a.sid["usera"] == "" || a.sid["userb"] == "" {
		c.t.Fatalf("no SIDs: owner %q, group %q, users %v", a.owner, a.group, a.sid)
	}
	return a
}

// aclSambaTool is method (a): samba-tool ntacl set, from the package that has samba-tool.
func (c *capture) aclSambaTool(a aclEnv) {
	c.t.Helper()
	block("acl-a-apk-add-samba-dc", c.result(c.tool("apk"), "add", "--quiet", "samba-dc"))
	st := ""
	for _, dir := range toolDirs {
		if p := filepath.Join(dir, "samba-tool"); exists(p) {
			st = p
			break
		}
	}
	if st == "" {
		block("acl-a-samba-tool", "samba-tool: none\n")
		return
	}
	block("acl-a-samba-tool", c.sh("apk info -W "+st)+c.result(st, "--version"))
	help := c.result(st, "ntacl", "set", "--help")
	block("acl-a-help", help)
	sddl := fmt.Sprintf("O:%sG:%sD:P(A;OICI;0x%08x;;;%s)(A;OICI;0x%08x;;;%s)", a.owner, a.group, acl.MaskChange,
		a.sid["usera"], acl.MaskRead, a.sid["userb"])
	out, st1 := c.run(st, "ntacl", "set", sddl, "/srv/acl/sub", "--use-s3fs", "--service=acl")
	block("acl-a-set", withStatus(out, st1))
	if st1 != 0 {
		// A standalone server has a local SID but no domain SID in secrets.tdb; try again once one is stored there.
		net := c.tool("net")
		local, _ := c.run(net, "getlocalsid")
		sid := regexp.MustCompile(`S-1-5-21-[0-9-]+`).FindString(local)
		text := c.result(net, "getdomainsid") + c.result(net, "setdomainsid", sid) + c.result(net, "getdomainsid")
		out, st1 = c.run(st, "ntacl", "set", sddl, "/srv/acl/sub", "--use-s3fs", "--service=acl")
		block("acl-a-set-with-domain-sid", text+withStatus(out, st1))
	}
	if strings.Contains(help, "--recursive") {
		block("acl-a-set-recursive", c.result(st, "ntacl", "set", sddl, "/srv/acl/sub", "--use-s3fs", "--service=acl",
			"--recursive"))
	}
	get := ""
	for _, p := range strings.Fields(aclTree) {
		get += "--- " + p + "\n" + c.result(st, "ntacl", "get", "--as-sddl", "--use-s3fs", "--service=acl", "/srv/acl/"+p)
	}
	block("acl-a-get", get)
	block("acl-a-readback", c.readback("acl"))
	block("acl-a-stats", c.treeStats("/srv/acl"))
}

// aclSmbcacls is method (b): smbcacls as the internal account, the ACEs inheritable and propagated to the existing tree.
func (c *capture) aclSmbcacls(a aclEnv) {
	c.t.Helper()
	smbcacls := c.tool("smbcacls")
	block("acl-b-help", c.result(smbcacls, "--help"))
	set := fmt.Sprintf("ACL:%s:ALLOWED/OI|CI/CHANGE,ACL:%s:ALLOWED/OI|CI/READ", a.sid["usera"], a.sid["userb"])
	// --propagate-inheritance refuses a folder that inherits; -I remove makes it protected first.
	block("error-acl-b-set-inheriting", c.cacls(`acl\sub`, "--propagate-inheritance", "--set", set))
	block("acl-b-inherit-remove", c.cacls(`acl\sub`, "-I", "remove")+c.cacls(`acl\sub`)+xattrs("/srv/acl/sub"))
	block("acl-b-set", c.cacls(`acl\sub`, "--propagate-inheritance", "--set", set))
	block("acl-b-readback", c.readback("acl"))
	block("acl-b-stats", c.treeStats("/srv/acl"))
	block("acl-b-password-stdin", c.resultIn(aclPassword+"\n", smbcacls, "//127.0.0.1/"+aclAdmin, `acl\sub`, "-U",
		aclAccount, "--numeric"))
}

// writeNTACL writes security.NTACL from Go with the encoder of the extension (package acl): the ACEs, protected, on dir,
// and the inherited ones on everything below it (links are not followed). It returns a line per path, and whether every
// path got its attribute.
func (c *capture) writeNTACL(dir string, a aclEnv, aces []acl.ACE) (string, bool) {
	c.t.Helper()
	text, ok := "", true
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			text += p + ": a link, skipped\n"
			return nil
		}
		own, control := aces, acl.ControlProtected
		if p != dir {
			own, control = acl.Inherited(aces, d.IsDir()), acl.ControlAutoInherited
		}
		blob, err := acl.NTACL(a.owner, a.group, control, own)
		if err != nil {
			return err
		}
		if err := unix.Lsetxattr(p, ntaclName, blob, 0); err != nil {
			text += fmt.Sprintf("%s: %v\n", p, err)
			ok = false
			return nil
		}
		text += fmt.Sprintf("%s: %d bytes\n", p, len(blob))
		return nil
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return text, ok
}

// setNTACL is writeNTACL for the probes after the methods, which build on the ACL: a path without its attribute fails
// the test.
func (c *capture) setNTACL(dir string, a aclEnv, aces []acl.ACE) string {
	c.t.Helper()
	text, ok := c.writeNTACL(dir, a, aces)
	if !ok {
		c.t.Errorf("%s is missing below %s:\n%s", ntaclName, dir, text)
	}
	return text
}

// resetACL removes security.NTACL from everything below root.
func (c *capture) resetACL(root string) string {
	c.t.Helper()
	text := ""
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := unix.Lremovexattr(p, ntaclName); err != nil && !errors.Is(err, unix.ENODATA) {
			text += fmt.Sprintf("%s: %v\n", p, err)
		}
		return nil
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return text + "removed " + ntaclName + " below " + root + "\n"
}

// folderRights are the rights of sub: usera may change, userb may read, both inherited by what is below.
func folderRights(a aclEnv) []acl.ACE {
	return []acl.ACE{{Flags: oici, Mask: acl.MaskChange, SID: a.sid["usera"]},
		{Flags: oici, Mask: acl.MaskRead, SID: a.sid["userb"]}}
}

// aclGo is method (c): security.NTACL written from Go on sub and below it. A path the kernel refuses is a result of the
// method; whether smbd reads the ACL back decides (storedACL).
func (c *capture) aclGo(a aclEnv) {
	c.t.Helper()
	text, _ := c.writeNTACL("/srv/acl/sub", a, folderRights(a))
	block("acl-c-set", text)
	block("acl-c-readback", c.readback("acl"))
	block("acl-c-stats", c.treeStats("/srv/acl"))
}

// storedACL says whether smbd reads back an ACL on sub with usera's and userb's ACEs. Only then do the results of the
// users say anything about the ACL: an ACL that smbd cannot read refuses all three alike as well.
func (c *capture) storedACL(a aclEnv) bool {
	c.t.Helper()
	back := c.cacls(`acl\sub`)
	return strings.Contains(back, "\nACL:"+a.sid["usera"]+":") && strings.Contains(back, "\nACL:"+a.sid["userb"]+":")
}

// gateAnswer is the answer of the gate from the outcomes of the methods whose ACL smbd read back: yes when the rights
// held per user with one of them, no when the three users got the same with one of them, and unknown when no such
// method is left.
func gateAnswer(outcomes []outcome) string {
	answer := "unknown"
	for _, o := range outcomes {
		if o.perUser() {
			return "yes"
		}
		if o.alike() {
			answer = "no"
		}
	}
	return answer
}

var subListed = regexp.MustCompile(`(?m)^\s+sub\s+D`)

// aclEnforce tries, as usera, userb and userc, to list the share's root and sub, and to read and write in sub, and
// returns what worked.
func (c *capture) aclEnforce(label, share, root string) outcome {
	c.t.Helper()
	smbclient := c.tool("smbclient")
	ok := map[string]bool{}
	text := ""
	for _, u := range aclUsers {
		k := u[0][len(u[0])-1:]
		for _, t := range []struct{ what, cmds string }{
			{"list-root", "ls"},
			{"list-sub", `ls sub\*`},
			{"read", `get sub\old.txt /tmp/acl-` + k + `.txt`},
			{"write", `put /tmp/local.txt sub\` + k + "-" + label + ".txt"},
			{"mkdir", `mkdir sub\` + k + "-" + label + "-dir"},
		} {
			out, st := c.run(smbclient, "//127.0.0.1/"+share, "-U", u[0]+"%"+u[1], "-c", t.cmds)
			good := st == 0 && !strings.Contains(out, "NT_STATUS_")
			ok[k+"-"+t.what] = good
			if t.what == "list-root" {
				ok[k+"-sees-sub"] = subListed.MatchString(out)
			}
			text += fmt.Sprintf("--- %s as %s: %s\n%s", t.what, u[0], yesNo(good), withStatus(out, st))
		}
	}
	o := outcome{ok}
	text += "summary:\n"
	for _, k := range []string{"a", "b", "c"} {
		text += fmt.Sprintf("user%s: sees sub in the root: %s, lists sub: %s, reads: %s, writes: %s, mkdir: %s\n", k,
			yesNo(ok[k+"-sees-sub"]), yesNo(ok[k+"-list-sub"]), yesNo(ok[k+"-read"]), yesNo(ok[k+"-write"]),
			yesNo(ok[k+"-mkdir"]))
	}
	text += fmt.Sprintf("per-user NT ACLs (%s, share %s): %s\n", label, share, yesNo(o.perUser()))
	block("acl-enforce-"+label, text)
	news := ""
	for _, k := range []string{"a", "b", "c"} {
		for _, n := range []string{k + "-" + label + ".txt", k + "-" + label + "-dir"} {
			p := filepath.Join(root, "sub", n)
			if exists(p) {
				news += c.stats(p) + xattrs(p) + c.cacls(strings.ReplaceAll(strings.TrimPrefix(p, "/srv/"), "/", `\`))
			}
		}
	}
	block("acl-enforce-"+label+"-new", news)
	return o
}

// aclToken gives the owner of the folder an ACE with full rights on sub as well: if smbd checks the token of the user
// it works as (force user), all three users may then write there.
func (c *capture) aclToken(a aclEnv) outcome {
	c.t.Helper()
	aces := append(folderRights(a), acl.ACE{Flags: oici, Mask: maskFull, SID: a.owner})
	block("acl-owner-ace-set", c.setNTACL("/srv/acl/sub", a, aces)+c.cacls(`acl\sub`))
	o := c.aclEnforce("owner-ace", "acl", "/srv/acl")
	block("acl-owner-ace-reset", c.resetACL("/srv/acl"))
	return o
}

// aclWithoutForceUser sets the same rights on a copy of the tree whose folders and files the group may write, shared
// without force user: smbd works as the user himself, force group gives him the group of the folder, and inherit owner
// gives new files the owner of the folder.
func (c *capture) aclWithoutForceUser(a aclEnv) outcome {
	c.t.Helper()
	root := "/srv/acl-g"
	c.makeTree(root)
	c.sh("chmod -R g+w " + root)
	c.setConf(globalConf(), aclShare("acl", "/srv/acl"), adminShare,
		section("acl-g", "path = "+root, "force group = "+owner, "inherit owner = unix only",
			"valid users = usera userb userc", "write list = usera userb userc", "vfs objects = acl_xattr",
			"acl_xattr:ignore system acls = yes", "hide unreadable = yes"))
	block("acl-g-set", c.setNTACL(root+"/sub", a, folderRights(a)))
	block("acl-g-readback", c.readback("acl-g"))
	o := c.aclEnforce("g", "acl-g", root)
	block("acl-g-stats", c.treeStats(root))
	return o
}

// aclRecycle puts an ACL on #recycle that lets only the writer (usera) in, and lists the share as the reader (userb)
// and the writer; the writer's deletions have to reach #recycle still.
func (c *capture) aclRecycle(a aclEnv) {
	c.t.Helper()
	root := "/srv/acl-rb"
	c.folder(root)
	c.folder(root + "/dir")
	c.oldFile(root+"/dir/one.txt", "one\n")
	c.oldFile(root+"/dir/two.txt", "two\n")
	c.setConf(globalConf(), adminShare,
		ownedShare("acl-rb", root, "valid users = usera userb", "read list = userb", "write list = usera",
			"vfs objects = acl_xattr recycle", "acl_xattr:ignore system acls = yes", "hide unreadable = yes",
			"recycle:repository = #recycle", "recycle:keeptree = yes", "recycle:versions = yes", "recycle:touch = yes"))
	smb := func(user, pw, cmds string) string {
		return "--- " + user + ": " + cmds + "\n" + c.smb("acl-rb", user, pw, cmds)
	}
	text := smb("usera", pwA, `cd dir; del one.txt`) + c.sh("ls -laR '"+root+"/#recycle' 2>&1; true")
	if !exists(root + "/#recycle") {
		block("acl-recycle-writer-only", text+"no #recycle: the deletion did not reach it\n")
		return
	}
	text += c.setNTACL(root+"/#recycle", a, []acl.ACE{{Flags: oici, Mask: maskFull, SID: a.sid["usera"]}})
	look := func() string {
		return smb("userb", pwB, "ls") + smb("userb", pwB, `ls #recycle\*`) + smb("usera", pwA, "ls") +
			smb("usera", pwA, `ls #recycle\*`)
	}
	text += look() + smb("usera", pwA, `cd dir; del two.txt`) + c.sh("ls -laR '"+root+"/#recycle' 2>&1; true") +
		xattrs(root+"/#recycle/dir/two.txt")
	block("acl-recycle-writer-only", text)
	text = c.setNTACL(root+"/#recycle", a, []acl.ACE{{Flags: oici, Mask: maskFull, SID: a.sid["usera"]},
		{Flags: oici, Mask: maskFull, SID: a.owner}})
	block("acl-recycle-with-owner", text+look())
}
