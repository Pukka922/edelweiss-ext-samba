package tool_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/tool"
)

func write(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setup is a host with /mnt/disk mounted and a share "My Share" on it, owned by an account, and the extension's config.
func setup(t *testing.T) (root string, tl tool.Tool, stdout, stderr *bytes.Buffer) {
	t.Helper()
	root = t.TempDir()
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n")
	if err := os.MkdirAll(filepath.Join(root, "mnt/disk/data"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "etc/passwd", fmt.Sprintf("myUser:x:%d:%d::/home/myUser:/bin/ash\n", os.Getuid(), os.Getgid()))
	write(t, root, "etc/group", fmt.Sprintf("myGroup:x:%d:\n", os.Getgid()))
	fsid, err := paths.FsidOf(host.FS{Root: root}, "/mnt/disk")
	if err != nil {
		t.Fatal(err)
	}
	cfg := samba.Config{Shares: []samba.Share{{Name: "My Share", Path: "/mnt/disk/data", Mount: "/mnt/disk", Fsid: fsid}}}
	write(t, root, samba.ConfigPath, string(cfg.JSON()))
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	return root, tool.Tool{FS: host.FS{Root: root}, Stdout: stdout, Stderr: stderr}, stdout, stderr
}

func TestCheckShareOK(t *testing.T) {
	_, tl, stdout, stderr := setup(t)
	for _, name := range []string{"My Share", "my share", "MY SHARE"} { // Samba does not tell the cases apart
		if status := tl.Run(context.Background(), []string{"check-share", "--name", name}); status != 0 {
			t.Errorf("--name %q: status %d, stderr %q", name, status, stderr)
		}
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("output on success: %q %q", stdout, stderr)
	}
}

// The disk is unplugged: its mount point is an empty directory, so the connection is refused.
func TestCheckShareRefusesUnmounted(t *testing.T) {
	root, tl, _, stderr := setup(t)
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n")
	if status := tl.Run(context.Background(), []string{"check-share", "--name", "My Share"}); status != tool.StatusFailed {
		t.Errorf("status %d, want %d", status, tool.StatusFailed)
	}
	if got := stderr.String(); got != "check-share: My Share: unmounted\n" {
		t.Errorf("stderr = %q", got)
	}
}

// It fails closed: whatever it cannot confirm refuses the connection, with one line of reason.
func TestCheckShareFailsClosed(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(root string)
		args   []string
		status int
		stderr string
	}{
		{"unknown share", nil, []string{"check-share", "--name", "Other"}, 1, "no such share"},
		{"not set up", func(root string) { os.Remove(filepath.Join(root, samba.ConfigPath)) },
			[]string{"check-share", "--name", "My Share"}, 1, "not set up"},
		{"unreadable config", func(root string) { write(t, root, samba.ConfigPath, "{not json") },
			[]string{"check-share", "--name", "My Share"}, 1, "config cannot be read"},
		{"missing folder", func(root string) { os.Remove(filepath.Join(root, "mnt/disk/data")) },
			[]string{"check-share", "--name", "My Share"}, 1, "My Share: missing"},
		{"no mount recorded", func(root string) { rewrite(t, root, func(c *samba.Config) { c.Shares[0].Mount = "" }) },
			[]string{"check-share", "--name", "My Share"}, 1, "My Share: unmounted"},
		{"another disk on the mount point", func(root string) {
			write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\ntmpfs /mnt/disk tmpfs rw 0 0\n")
		}, []string{"check-share", "--name", "My Share"}, 1, "My Share: unmounted"},
		{"two shares differ only in case", func(root string) {
			rewrite(t, root, func(c *samba.Config) {
				c.Shares = append(c.Shares, c.Shares[0])
				c.Shares[1].Name = "MY SHARE"
			})
		}, []string{"check-share", "--name", "my share"}, 1, "ambiguous"},
		{"no name", nil, []string{"check-share"}, 2, "--name is required"},
		{"empty name", nil, []string{"check-share", "--name", ""}, 2, "--name is required"},
		{"extra argument", nil, []string{"check-share", "--name", "My", "Share"}, 2, "unexpected argument"},
		{"unknown flag", nil, []string{"check-share", "--nam", "x"}, 2, "flag provided but not defined"},
		{"unknown subcommand", nil, []string{"nope"}, 2, "unknown subcommand"},
		{"no subcommand", nil, nil, 2, "no subcommand"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, tl, stdout, stderr := setup(t)
			if c.mutate != nil {
				c.mutate(root)
			}
			status := tl.Run(context.Background(), c.args)
			if status != c.status || !strings.Contains(stderr.String(), c.stderr) || strings.Count(stderr.String(), "\n") != 1 || stdout.Len() != 0 {
				t.Errorf("status %d (want %d), stderr %q (want %q in one line), stdout %q", status, c.status, stderr, c.stderr, stdout)
			}
		})
	}
}

// A client in a network of a Docker or libvirt bridge that is there now is refused (read at the connect: a bridge made
// after the last change of smb.conf is not in its hosts allow), also as an IPv4 address in IPv6; any other client is let
// in. A bridge that is chosen is no such network. What cannot be read refuses: an address that is none, an empty one, the
// interfaces. Without --client (the root preexec of an older smb.conf) the client is not looked at.
func TestCheckShareRefusesBridgeClient(t *testing.T) {
	root, tl, _, stderr := setup(t)
	reads := 0
	ifaces := []samba.Iface{
		{Name: "br-1a2b", Bridge: true, Addrs: []samba.Addr{{Iface: "br-1a2b", CIDR: "172.18.0.1/16", Private: true}}},
		{Name: "eth0", Addrs: []samba.Addr{{Iface: "eth0", CIDR: "192.168.1.10/24", Private: true}}},
	}
	var ifacesErr error
	tl.Ifaces = func() ([]samba.Iface, error) { reads++; return ifaces, ifacesErr }
	check := func(args ...string) (int, string) {
		stderr.Reset()
		status := tl.Run(context.Background(), append([]string{"check-share", "--name", "My Share"}, args...))
		return status, stderr.String()
	}
	for _, c := range []struct {
		client, stderr string
		status         int
	}{
		{"192.168.1.20", "", 0},
		{"127.0.0.1", "", 0},
		{"172.19.0.5", "", 0},
		{"fe80::1%eth0", "", 0},
		{"172.18.0.5", "check-share: My Share: the client 172.18.0.5 is in 172.18.0.0/16, a network of a Docker or libvirt bridge\n", 1},
		{"::ffff:172.18.0.5", "check-share: My Share: the client 172.18.0.5 is in 172.18.0.0/16, a network of a Docker or libvirt bridge\n", 1},
		{"not-an-address", "check-share: My Share: the client address \"not-an-address\" cannot be read\n", 1},
		{"", "check-share: My Share: the client address \"\" cannot be read\n", 1},
	} {
		if status, out := check("--client", c.client); status != c.status || out != c.stderr {
			t.Errorf("--client %q: status %d, stderr %q; want %d, %q", c.client, status, out, c.status, c.stderr)
		}
	}
	ifacesErr = errors.New("no netlink")
	if status, out := check("--client", "192.168.1.20"); status != 1 || !strings.Contains(out, "the network interfaces cannot be read: no netlink") {
		t.Errorf("interfaces that cannot be read: %d %q", status, out)
	}
	ifacesErr = nil
	rewrite(t, root, func(c *samba.Config) { c.Settings.Interfaces = []string{"br-1a2b", "eth0"} })
	if status, out := check("--client", "172.18.0.5"); status != 0 {
		t.Errorf("a chosen bridge: %d %q", status, out)
	}
	reads = 0
	if status, out := check(); status != 0 || reads != 0 {
		t.Errorf("without --client: %d %q, the interfaces read %d times", status, out, reads)
	}
}

// rewrite changes the config that setup wrote.
func rewrite(t *testing.T, root string, f func(c *samba.Config)) {
	t.Helper()
	cfg, ok, err := samba.Load(host.FS{Root: root})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	f(&cfg)
	write(t, root, samba.ConfigPath, string(cfg.JSON()))
}

// The name smbd gives is the configured one: an exact match wins, and only then a single match without case.
func TestCheckShareExactFirst(t *testing.T) {
	root, tl, _, stderr := setup(t)
	rewrite(t, root, func(c *samba.Config) {
		c.Shares = append(c.Shares, c.Shares[0])
		c.Shares[1].Name = "MY SHARE"
		c.Shares[1].Mount = "" // this one is never available
	})
	if status := tl.Run(context.Background(), []string{"check-share", "--name", "My Share"}); status != 0 {
		t.Errorf("exact name: status %d, %q", status, stderr)
	}
	stderr.Reset()
	if status := tl.Run(context.Background(), []string{"check-share", "--name", "MY SHARE"}); status != 1 || !strings.Contains(stderr.String(), "MY SHARE: unmounted") {
		t.Errorf("the other exact name: status %d, %q", status, stderr)
	}
}
