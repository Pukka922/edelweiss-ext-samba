package actions_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// shareOfRoot is a share of the folder on the file system of /, as it is recorded when it is made.
func (f *fixture) shareOfRoot(name, path string) samba.Share {
	f.t.Helper()
	fsid, err := paths.FsidOf(f.env.FS, "/")
	if err != nil {
		f.t.Fatal(err)
	}
	return samba.Share{Name: name, Path: path, Mount: "/", Fsid: fsid, Rights: map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}}
}

func folderRightsOf(name, folder string, rights map[string]samba.Right) actions.Request {
	return actions.Request{Kind: "folder-rights", Name: name, Folder: folder, Rights: rights, Convert: true}
}

// Folder rights are not for a share of a folder of the system or of root: the conversion would make every file in it
// writable for the group. Turning them on and setting one is refused (folder_rights_unsafe, with the warnings among system
// and root_owned) and nothing is planned; taking a folder's rights away and applying them again stay possible. A share
// without those warnings is not affected.
func TestFolderRightsUnsafe(t *testing.T) {
	rights := map[string]samba.Right{"anna": samba.Read}
	for _, c := range []struct {
		name  string
		share string // the folder
		root  bool   // it belongs to root
		want  []string
	}{
		{"system", "/etc", false, []string{"system"}},
		{"root_owned", "/mnt/disk/data", true, []string{"root_owned"}},
		{"both", "/etc", true, []string{"system", "root_owned"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.root {
				f.asRoot(c.share)
			}
			f.with(f.shareOfRoot("Risky", c.share))
			if c.share == "/mnt/disk/data" {
				sh := f.fotos()
				sh.Name = "Risky"
				f.with(sh)
			}
			put(t, f.root, "etc/samba/x", "x")
			folder := "sub"
			if c.share == "/etc" {
				folder = "samba"
			}
			e := f.refused(folderRightsOf("Risky", folder, rights))
			code(t, e, actions.CodeFolderRightsUnsafe, map[string]any{"warnings": c.want})

			// the share that already has folder rights keeps them: apply again and taking rights away work, setting do not
			sh := f.env.Config.Shares[0]
			sh.PerUser, sh.Folders = true, map[string]map[string]samba.Right{folder: rights}
			f.with(sh)
			f.plan(actions.Request{Kind: "folder-rights", Name: "Risky", Folder: ""})
			f.plan(actions.Request{Kind: "folder-rights", Name: "Risky", Folder: folder, Inherit: true})
			code(t, f.refused(folderRightsOf("Risky", folder, map[string]samba.Right{"anna": samba.Write})), actions.CodeFolderRightsUnsafe, nil)
		})
	}

	// a share without those warnings is not affected, with or without a mount below it
	f := newFixture(t)
	f.with(f.fotos())
	f.env.Mounts = append(f.env.Mounts, host.Mount{Device: "/dev/sdd1", Point: "/mnt/disk/data/sub/deep", Type: "ext4"})
	f.plan(folderRightsOf("Fotos", "sub", rights))
}

// A folder whose absolute path is a kernel folder is refused on the routes that take a share and a folder, whatever share
// it is in: the folder proc of a share of /, and a folder on a pseudo mount below a share of /var.
func TestFolderRoutesKernel(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, "var/lib/nfs/rpc_pipefs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"dev/shm", "sys/fs"} {
		if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.env.Mounts = append(f.env.Mounts, host.Mount{Device: "rpc_pipefs", Point: "/var/lib/nfs/rpc_pipefs", Type: "rpc_pipefs"})
	whole, vr := f.shareOfRoot("Root", "/"), f.shareOfRoot("Var", "/var")
	f.with(whole, vr)
	rights := map[string]samba.Right{"anna": samba.Read}
	for _, c := range []struct {
		req   actions.Request
		field string
	}{
		{folderRightsOf("Root", "proc", rights), "folder"},
		{folderRightsOf("Root", "proc/1", rights), "folder"},
		{folderRightsOf("Root", "dev/shm", rights), "folder"},
		{folderRightsOf("Var", "lib/nfs/rpc_pipefs", rights), "folder"},
		{actions.Request{Kind: "recycle-restore", Name: "Root", Item: "sys/fs/x"}, "item"},
		{actions.Request{Kind: "recycle-restore", Name: "Var", Item: "lib/nfs/rpc_pipefs/x"}, "item"},
	} {
		e := f.refused(c.req)
		code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "kernel"})
		if e.Field != c.field {
			t.Errorf("%s %s%s: field %q, want %q", c.req.Kind, c.req.Folder, c.req.Item, e.Field, c.field)
		}
	}
}

// Folder rights are no use on a home directory either: the conversion makes everything in it writable for the group, and
// sshd then refuses the key of the account. A share that is, or holds, the home directory of a user of /etc/passwd, or
// holds a .ssh directly in it, is refused with home among the warnings; a folder inside a home is not.
func TestFolderRightsHome(t *testing.T) {
	rights := map[string]samba.Right{"anna": samba.Read}
	for _, c := range []struct {
		share string
		sub   string // a folder in it
		want  []string
	}{
		{"/home/myUser", "Daten", []string{"home"}},
		{"/home", "myUser", []string{"home"}},
		{"/home/myUser/Daten", "sub", nil},
		{"/srv/x", "sub", []string{"home"}}, // holds a .ssh
	} {
		t.Run(c.share, func(t *testing.T) {
			f := newFixture(t)
			for _, d := range []string{"home/myUser/Daten/sub", "srv/x/sub", "srv/x/.ssh"} {
				if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			f.with(f.shareOfRoot("Risky", c.share))
			if c.want == nil {
				f.plan(folderRightsOf("Risky", c.sub, rights))
				return
			}
			e := f.refused(folderRightsOf("Risky", c.sub, rights))
			code(t, e, actions.CodeFolderRightsUnsafe, map[string]any{"warnings": c.want})

			// taking rights away and applying them again stay possible
			sh := f.env.Config.Shares[0]
			sh.PerUser, sh.Folders = true, map[string]map[string]samba.Right{c.sub: rights}
			f.with(sh)
			f.plan(actions.Request{Kind: "folder-rights", Name: "Risky", Folder: ""})
			f.plan(actions.Request{Kind: "folder-rights", Name: "Risky", Folder: c.sub, Inherit: true})
		})
	}

	// a .ssh counts only directly in the folder, and so do only the homes that exist and are no placeholder
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, "srv/y/deep/.ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, f.root, accounts.PasswdFile, "myUser:x:"+itoa(os.Getuid())+":"+itoa(os.Getgid())+"::/srv/y/missing:/bin/ash\n"+
		"nobody:x:65534:65534::/:/sbin/nologin\nnull:x:1:1::/dev/null:/sbin/nologin\n"+
		"sshd:x:22:22::/var/empty:/sbin/nologin\nghost:x:1001:1001::/home/ghost:/bin/ash\n")
	f.with(f.shareOfRoot("Risky", "/srv/y"))
	f.plan(folderRightsOf("Risky", "deep", rights))
}
