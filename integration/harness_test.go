//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

var ctx = context.Background()

// exe runs the commands as the extension runs them: no shell, "/" as the working directory, the environment of the
// extension (run.Env), and no stdin unless the command gives one.
var exe = run.Exec{Env: run.Env}

// toolDirs are the directories the extension looks for its tools in, in this order; the first hit counts.
var toolDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// findTool is the first executable file called name in toolDirs.
func findTool(t *testing.T, name string) string {
	t.Helper()
	for _, dir := range toolDirs {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	t.Fatalf("%s is in none of %v", name, toolDirs)
	return ""
}

// noFinalNewline lists the blocks whose text lacked the final newline (block added one).
var noFinalNewline []string

// block writes a block of a capture to the test log: a line "=== name ===" and the text below it. The output of the
// commands reaches the log unchanged; a text without final newline gets one, and its name goes to noFinalNewline.
func block(name, text string) {
	fmt.Printf("=== %s ===\n%s", name, text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Println()
		noFinalNewline = append(noFinalNewline, name)
	}
}

// withStatus is out below a line with the exit status, as errors.txt holds it.
func withStatus(out string, status int) string {
	return fmt.Sprintf("exit status: %d\n%s", status, out)
}

// fileText is the content of the file, or a line that says why it could not be read.
func fileText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(cannot read: %v)\n", err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// capture runs the commands of a test and keeps the exit status of each.
type capture struct {
	t        *testing.T
	statuses []string
}

func newCapture(t *testing.T) *capture { return &capture{t: t} }

// exec runs cmd (5 minutes at most) and returns what it printed (stdout and stderr together) and its exit status. A
// command that did not run, or was killed, fails the test. The statuses never show the stdin.
func (c *capture) exec(cmd run.Cmd) (string, int) {
	c.t.Helper()
	if cmd.Timeout == 0 {
		cmd.Timeout = 5 * time.Minute
	}
	out, err := exe.Run(ctx, cmd)
	got := 0
	if err != nil {
		var e *run.Error
		if !errors.As(err, &e) || e.Status < 0 {
			c.t.Fatalf("the command did not run: %v", err)
		}
		got = e.Status
	}
	line := fmt.Sprintf("%3d  %s", got, cmd)
	if cmd.Stdin != "" {
		line += "  (stdin given)"
	}
	c.statuses = append(c.statuses, line)
	return out, got
}

// run runs path with args; runIn gives the command stdin as well.
func (c *capture) run(path string, args ...string) (string, int) {
	c.t.Helper()
	return c.exec(run.Cmd{Path: path, Args: args})
}

func (c *capture) runIn(stdin, path string, args ...string) (string, int) {
	c.t.Helper()
	return c.exec(run.Cmd{Path: path, Args: args, Stdin: stdin})
}

// step is run for a command that has to end with status 0; stepIn with stdin.
func (c *capture) step(path string, args ...string) string {
	c.t.Helper()
	out, got := c.run(path, args...)
	return c.ok(path, args, out, got)
}

func (c *capture) stepIn(stdin, path string, args ...string) string {
	c.t.Helper()
	out, got := c.runIn(stdin, path, args...)
	return c.ok(path, args, out, got)
}

func (c *capture) ok(path string, args []string, out string, got int) string {
	c.t.Helper()
	if got != 0 {
		c.t.Errorf("%s %s: exit status %d\n%s", path, strings.Join(args, " "), got, out)
	}
	return out
}

// result is the output of a command below its exit status, whatever the status is: a refusal is a result too.
func (c *capture) result(path string, args ...string) string {
	c.t.Helper()
	return withStatus(c.run(path, args...))
}

func (c *capture) resultIn(stdin, path string, args ...string) string {
	c.t.Helper()
	return withStatus(c.runIn(stdin, path, args...))
}

// sh is a step of /bin/sh -c script: helpers (ls, stat, grep) that are no commands of the extension.
func (c *capture) sh(script string) string {
	c.t.Helper()
	return c.step("/bin/sh", "-c", script)
}

// tool is the path of the tool called name (findTool).
func (c *capture) tool(name string) string {
	c.t.Helper()
	return findTool(c.t, name)
}

// finish ends the log of a test: the exit status of every command, the blocks that lacked the final newline, "end".
func (c *capture) finish() {
	block("statuses", strings.Join(c.statuses, "\n")+"\n")
	block("blocks-without-final-newline", strings.Join(noFinalNewline, "\n")+"\n")
	block("end", "")
}

// poll checks cond every interval until it is true or limit has passed; it returns how long that took and whether cond
// became true.
func poll(limit, interval time.Duration, cond func() bool) (time.Duration, bool) {
	start := time.Now()
	for {
		if cond() {
			return time.Since(start), true
		}
		if time.Since(start) > limit {
			return time.Since(start), false
		}
		time.Sleep(interval)
	}
}

// Samba.

const (
	smbConf = "/etc/samba/smb.conf"
	// marker is the primary group of the accounts the extension makes.
	marker = "edelweiss-samba"
	// owner is the host user (UID 1000) that owns the shared folders; smbd works as this user (force user).
	owner = "myUser"
)

// globalConf is the [global] section of the test configurations: a standalone server, SMB3 only, no NetBIOS, no
// printers, no guest, a log per client in /var/log/samba; the extra lines are added at the end.
func globalConf(extra ...string) string {
	return section("global", append([]string{
		"server role = standalone server",
		"workgroup = WORKGROUP",
		"netbios name = CAPTURE",
		"server min protocol = SMB3",
		"disable netbios = yes",
		"smb ports = 445",
		"map to guest = never",
		"load printers = no",
		"printing = bsd",
		"printcap name = /dev/null",
		"disable spoolss = yes",
		"log file = /var/log/samba/log.%m",
		"max log size = 0",
	}, extra...)...)
}

// section is a section of smb.conf: "[name]" and the lines, each indented by a tab.
func section(name string, lines ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", name)
	for _, l := range lines {
		fmt.Fprintf(&b, "\t%s\n", l)
	}
	return b.String()
}

// ownedShare is the section of a share of the folder path as the extension writes one: smbd works as the owner of
// the folder; the lines follow.
func ownedShare(name, path string, lines ...string) string {
	return section(name, append([]string{"path = " + path, "force user = " + owner, "force group = " + owner}, lines...)...)
}

// setConf writes smb.conf (the sections joined by empty lines), checks it with testparm -s (a failure, or a parameter
// that Samba does not know, fails the test) and has the running smbd reload it, then gives it a second: smbcontrol only
// sends the message. smbcontrol smbd fails when no smbd runs (smbcontrol all would not).
func (c *capture) setConf(sections ...string) {
	c.t.Helper()
	writeFile(c.t, smbConf, strings.Join(sections, "\n"))
	if out := c.step(c.tool("testparm"), "-s", smbConf); strings.Contains(out, "nknown parameter") {
		c.t.Errorf("testparm -s %s: a parameter that Samba does not know\n%s", smbConf, out)
	}
	c.step(c.tool("smbcontrol"), "smbd", "reload-config")
	time.Sleep(time.Second)
}

// folder makes the directory path (and its parents) owned by the owner with mode 0755.
func (c *capture) folder(path string) {
	c.t.Helper()
	c.sh("mkdir -p '" + path + "' && chown " + owner + ":" + owner + " '" + path + "' && chmod 0755 '" + path + "'")
}

// ownedFile writes a file of the owner (mode 0644) with the text.
func (c *capture) ownedFile(path, text string) {
	c.t.Helper()
	writeFile(c.t, path, text)
	c.sh("chown " + owner + ":" + owner + " '" + path + "'")
}

// hostUser makes the owner of the shared folders: a host user with UID 1000 and a group of the same name.
func (c *capture) hostUser() string {
	c.t.Helper()
	return c.result(c.tool("busybox"), "adduser", "-D", "-u", "1000", "-s", "/bin/sh", owner)
}

// markerGroup makes the system group of the accounts the extension makes.
func (c *capture) markerGroup() string {
	c.t.Helper()
	return c.result(c.tool("busybox"), "addgroup", "-S", marker)
}

// account makes a Linux account as the extension makes one for a pure Samba user: busybox adduser without a password
// and without a home directory, with the shell /sbin/nologin and the primary group marker.
func (c *capture) account(name string) string {
	c.t.Helper()
	return c.result(c.tool("busybox"), "adduser", "-D", "-H", "-s", "/sbin/nologin", "-G", marker, name)
}

// sambaUser makes the account and its Samba entry with the password (pdbedit -a -t, the password twice on stdin).
func (c *capture) sambaUser(name, pw string) {
	c.t.Helper()
	c.account(name)
	c.stepIn(pw+"\n"+pw+"\n", c.tool("pdbedit"), "-a", "-u", name, "-t")
}

// smb runs smbclient against the service on the loopback address as user with the password and the commands of -c
// (none when cmds is empty); the other arguments follow. It returns the output below the exit status.
func (c *capture) smb(service, user, pw, cmds string, args ...string) string {
	c.t.Helper()
	return c.smbAt("127.0.0.1", service, user, pw, cmds, args...)
}

func (c *capture) smbAt(addr, service, user, pw, cmds string, args ...string) string {
	c.t.Helper()
	a := []string{"//" + addr + "/" + service, "-U", user + "%" + pw}
	if cmds != "" {
		a = append(a, "-c", cmds)
	}
	return c.result(c.tool("smbclient"), append(a, args...)...)
}

// list is smbclient -L as user: the shares the server lists for the user.
func (c *capture) list(user, pw string, args ...string) string {
	c.t.Helper()
	return c.result(c.tool("smbclient"), append([]string{"-L", "//127.0.0.1", "-U", user + "%" + pw}, args...)...)
}

// listening says whether something accepts TCP connections on addr.
func listening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// startSamba starts the service samba with OpenRC and waits up to 30 s for smbd on port 445. It returns what
// rc-service printed and how long smbd took.
func (c *capture) startSamba() string {
	c.t.Helper()
	text := c.result(c.tool("rc-service"), "samba", "start")
	took, ok := poll(30*time.Second, 100*time.Millisecond, func() bool { return listening("127.0.0.1:445") })
	if !ok {
		c.t.Errorf("smbd does not listen on 127.0.0.1:445 30 s after rc-service samba start")
	}
	return text + fmt.Sprintf("smbd listens on 127.0.0.1:445: %v, %s after the start\n", ok, took.Round(100*time.Millisecond))
}

// samba writes the first smb.conf and the accounts every capture needs (the owner, the marker group), and starts
// samba; it returns what that printed.
func (c *capture) samba(sections ...string) string {
	c.t.Helper()
	text := "addgroup -S " + marker + ": " + c.markerGroup() + "adduser " + owner + ": " + c.hostUser()
	writeFile(c.t, smbConf, strings.Join(sections, "\n"))
	return text + c.startSamba()
}

// xattrs lists the extended attributes of path (without following a link): name and value in hex, one per line, sorted.
func xattrs(path string) string {
	buf := make([]byte, 64<<10)
	n, err := unix.Llistxattr(path, buf)
	if err != nil {
		return fmt.Sprintf("%s: (listxattr: %v)\n", path, err)
	}
	var names []string
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return path + ": (no extended attributes)\n"
	}
	var b strings.Builder
	for _, name := range names {
		v := make([]byte, 64<<10)
		m, err := unix.Lgetxattr(path, name, v)
		if err != nil {
			fmt.Fprintf(&b, "%s: %s (getxattr: %v)\n", path, name, err)
			continue
		}
		if m > 512 {
			fmt.Fprintf(&b, "%s: %s = %d bytes, the first 32: %s\n", path, name, m, hex.EncodeToString(v[:32]))
			continue
		}
		fmt.Fprintf(&b, "%s: %s = %s\n", path, name, hex.EncodeToString(v[:m]))
	}
	return b.String()
}

// stats lists owner, group, mode and the times of the paths (stat, the times in seconds since the epoch).
func (c *capture) stats(paths ...string) string {
	c.t.Helper()
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = "'" + p + "'"
	}
	return c.result("/bin/sh", "-c", "stat -c '%n %U:%G %a atime=%X mtime=%Y ctime=%Z' "+strings.Join(quoted, " "))
}

// The manifests of the data of before: what a change must leave as it is.

// entry is what a change must not change of a file but what compare allows: owner, group, mode, size, the times, the
// content and the extended attributes ("name=hex" lines, sorted).
type entry struct {
	mode, uid, gid      uint32
	size                int64
	mtime, atime, ctime unix.Timespec
	sum                 [32]byte
	xattrs              string
}

// attrsOf are the extended attributes of p (a link is not followed) as "name=hex" lines, sorted; what cannot be read is a
// line of its own, so that it never compares equal by chance.
func attrsOf(p string) string {
	buf := make([]byte, 64<<10)
	n, err := unix.Llistxattr(p, buf)
	if err != nil {
		return "listxattr: " + err.Error()
	}
	var lines []string
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name == "" {
			continue
		}
		v := make([]byte, 64<<10)
		m, err := unix.Lgetxattr(p, name, v)
		if err != nil {
			lines = append(lines, name+": "+err.Error())
			continue
		}
		lines = append(lines, name+"="+hex.EncodeToString(v[:m]))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// withoutAttrs are the lines of attrs (attrsOf) but those of the names.
func withoutAttrs(attrs string, names []string) string {
	var keep []string
	for _, l := range strings.Split(attrs, "\n") {
		if l != "" && !slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(l, n+"=") || strings.HasPrefix(l, n+":") }) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// manifest is the entry of every path below root, by path; links are not followed, and files are read without moving
// their access time. The access time of a folder is left out: the walk itself reads the folder.
func (c *capture) manifest(root string) map[string]entry {
	c.t.Helper()
	out := map[string]entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			return err
		}
		e := entry{mode: st.Mode, uid: st.Uid, gid: st.Gid, size: st.Size, mtime: st.Mtim, atime: st.Atim, ctime: st.Ctim,
			xattrs: attrsOf(p)}
		if d.IsDir() {
			e.atime = unix.Timespec{}
		}
		if d.Type().IsRegular() {
			f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NOATIME, 0)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			copy(e.sum[:], h.Sum(nil))
		}
		out[p] = e
		return nil
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

// delta is what may differ between two manifests for a path: the times named, the bits that have to have been added to
// the mode (nothing else of it may change), and the extended attributes named in attrs, which may change, come or go.
type delta struct {
	mtime, ctime, atime bool
	modeAdd             uint32
	attrs               []string
}

// compare fails the test for every path of before that after does not have, or has with an entry that differs beyond
// what allowed gives it (nil allows nothing). Paths for which skip is true are left out. It returns the paths compared.
func (c *capture) compare(label string, before, after map[string]entry, skip func(string) bool,
	allowed func(p string, e entry) delta) int {
	c.t.Helper()
	n := 0
	for _, p := range slices.Sorted(maps.Keys(before)) {
		if skip != nil && skip(p) {
			continue
		}
		n++
		b, a := before[p], after[p]
		if _, ok := after[p]; !ok {
			c.t.Errorf("%s: %s is gone", label, p)
			continue
		}
		var d delta
		if allowed != nil {
			d = allowed(p, b)
		}
		want := b
		want.mode |= d.modeAdd
		if d.mtime {
			want.mtime = a.mtime
		}
		if d.ctime {
			want.ctime = a.ctime
		}
		if d.atime {
			want.atime = a.atime
		}
		if len(d.attrs) > 0 {
			want.xattrs, a.xattrs = withoutAttrs(want.xattrs, d.attrs), withoutAttrs(a.xattrs, d.attrs)
		}
		if a != want {
			c.t.Errorf("%s: %s: %+v, want %+v", label, p, a, want)
		}
	}
	return n
}

// below reports whether p is dir or below it.
func below(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }
