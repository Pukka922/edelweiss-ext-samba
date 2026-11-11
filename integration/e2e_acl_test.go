//go:build integration

package integration

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The users and the files of TestEndToEndACL: anna and berta write in the share Team; in privat only anna, in lesen berta
// reads; carl has no right. bulkDirs times bulkFiles empty files make the conversion long enough to show its progress and
// to be cancelled. The margin: here the apply of the second run takes about 5.5 s, 2.7 times the 2-s tick at which the
// runner reads a step's progress; on storage that is much faster no step outlasts a tick, the check that progress was
// reported fails (deterministically, never at random), and the bulk has to grow.
const (
	pwBerta   = "berta-pw-1"
	bulkDirs  = 400
	bulkFiles = 1000
	team      = dataDisk + "/team"
)

// TestEndToEndACL is the folder rights through the API (SYS_ADMIN): the first folder right of Team is refused with
// needs_convert (its count stops at the cap, which the details say: capped); confirmed, the job converts with progress,
// is cancelled once, and runs to its end when it is asked for again. The rights hold per user for anna, berta and carl
// with smbclient, a restore into a folder that is gone below privat gets privat's rights, and the stat manifest of the
// data of before shows only the bits of the conversion: g+rw on the files of the share's group with one name, g+rwx on
// the folders of that group.
func TestEndToEndACL(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	e.linuxUser("ben", 1000, pwBen)
	e.setup()
	for _, u := range [][2]string{{"anna", pwAnna}, {"berta", pwBerta}, {"carl", pwCarl}} {
		e.sambaUser(u[0], u[1])
	}
	e.teamTree()
	e.addShare(samba.Share{Name: "Team", Path: team, Rights: map[string]samba.Right{"anna": samba.Write, "berta": samba.Write},
		Recycle: true})
	before := e.manifest(team)

	req := actions.Request{Kind: "folder-rights", Name: "Team", Folder: "privat", Rights: map[string]samba.Right{"anna": samba.Write, "berta": ""}}
	if d := e.refuse(req, 409, "needs_convert"); d["share"] != "Team" || d["files"] != float64(10000) || d["capped"] != true {
		t.Errorf("needs_convert: %v", d)
	}
	req.Convert = true
	// the progress that the state shows: a step writes 0 at its start, and what its tool reports every 2 s
	var progress []string
	reported := false
	watchProgress := func(s jobs.State) {
		if s.Progress == nil {
			return
		}
		if p := fmt.Sprintf("%s %.0f%%", s.Progress.Label, s.Progress.Percent); !slices.Contains(progress, p) {
			progress = append(progress, p)
		}
		reported = reported || s.Progress.Percent > 0
	}
	st := e.start(req)
	cancelledAt := ""
	end := e.await(st.ID, func(s jobs.State) {
		watchProgress(s)
		if cancelledAt == "" && s.Cancellable {
			e.ok(e.send("POST", "/jobs/"+st.ID+"/cancel", nil), 204, nil)
			cancelledAt = s.Steps[s.Step]
		}
	})
	if end.Status != jobs.Failed || end.Code != jobs.CodeCancelled || cancelledAt == "" {
		t.Fatalf("the cancelled job (cancelled at %q): %+v\n%s", cancelledAt, end, e.jobFiles(st.ID))
	}
	if sh := e.shareOf("Team"); sh.PerUser || len(sh.Folders) != 0 {
		t.Errorf("Team after the cancel: %+v", sh)
	}
	started := time.Now()
	st = e.start(req)
	end = e.await(st.ID, watchProgress)
	took := time.Since(started)
	if end.Status != jobs.Done {
		t.Fatalf("the job asked for again: %+v\n%s", end, e.jobFiles(st.ID))
	}
	if !reported {
		t.Errorf("no progress reported in %s: %v (no step outlasted the 2-s tick of the progress: the bulk is too small for this storage)",
			took, progress)
	}
	block("acl-jobs", fmt.Sprintf("cancelled at %s; the second run took %s; progress %v\n%s", cancelledAt, took.Round(time.Millisecond),
		progress, e.jobFiles(st.ID)))
	if sh := e.shareOf("Team"); !sh.PerUser || !reflect.DeepEqual(sh.Folders, map[string]map[string]samba.Right{"privat": {"anna": samba.Write}}) {
		t.Errorf("Team after the job: %+v", sh)
	}
	// every entry got security.NTACL (its change time), every folder the default ACL of the conversion; the bits only where
	// the group is the share folder's and a file has one name. No other attribute changed.
	ntacl, dflt := []string{"security.NTACL"}, []string{"security.NTACL", acl.DefaultACLAttr}
	e.compare(team, before, e.manifest(team), func(p string) bool { return below(p, team+"/"+recycle.Dir) },
		func(p string, en entry) delta {
			switch {
			case en.mode&unix.S_IFMT == unix.S_IFDIR && en.gid == 1000:
				return delta{modeAdd: 0o070, ctime: true, attrs: dflt}
			case en.mode&unix.S_IFMT == unix.S_IFDIR:
				return delta{ctime: true, attrs: dflt}
			case en.mode&unix.S_IFMT == unix.S_IFREG && en.gid == 1000 && !strings.HasSuffix(p, "/hard.txt") &&
				!strings.HasSuffix(p, "/alt.txt"):
				return delta{modeAdd: 0o060, ctime: true, attrs: ntacl}
			}
			return delta{ctime: true, attrs: ntacl}
		})

	e.change(actions.Request{Kind: "folder-rights", Name: "Team", Folder: "lesen", Rights: map[string]samba.Right{"anna": samba.Write, "berta": samba.Read}})
	e.teamAccess()
	e.teamRestore()
}

// teamTree makes the folder of Team (ben's) with data of before: files and folders of ben's group, a file of another group,
// a file with two names (hard.txt and alt.txt), and the bulk.
func (e *e2e) teamTree() {
	e.dir(team, 1000, 1000, 0o755)
	e.file(team+"/top.txt", "top\n", 1000, 1000, 0o644)
	e.file(team+"/fremd.txt", "fremd\n", 1000, 0, 0o644)
	e.file(team+"/hard.txt", "hard\n", 1000, 1000, 0o644)
	if err := os.Link(team+"/hard.txt", team+"/alt.txt"); err != nil {
		e.t.Fatal(err)
	}
	for _, d := range []string{"privat", "lesen"} {
		e.dir(team+"/"+d, 1000, 1000, 0o755)
		e.file(team+"/"+d+"/doc.txt", d+"\n", 1000, 1000, 0o644)
		e.own(team+"/"+d, 1000, 1000, 0o755)
	}
	e.dir(team+"/bulk", 1000, 1000, 0o755)
	for i := range bulkDirs {
		dir := fmt.Sprintf("%s/bulk/%03d", team, i)
		e.dir(dir, 1000, 1000, 0o755)
		for j := range bulkFiles {
			p := fmt.Sprintf("%s/%04d", dir, j)
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Lchown(p, 1000, 1000); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	e.own(team+"/bulk", 1000, 1000, 0o755)
	e.own(team, 1000, 1000, 0o755)
}

// teamAccess: anna lists, reads and writes in privat; berta does not see privat and is refused there, reads in lesen and
// may not write there, and writes in the share's folder; carl is refused.
func (e *e2e) teamAccess() {
	text := e.can("Team", "anna", pwAnna, `ls privat\*; get privat\doc.txt /tmp/doc.txt; put `+localFile+` privat\a.txt`)
	text += e.cannot("Team", "berta", pwBerta, `ls privat\*`, "NT_STATUS_ACCESS_DENIED")
	text += e.cannot("Team", "berta", pwBerta, `get privat\doc.txt /tmp/doc.txt`, "NT_STATUS_ACCESS_DENIED")
	ok, out := e.smbAt("127.0.0.1", "Team", "berta", pwBerta, "ls")
	text += out
	listed := func(name string) bool { return regexp.MustCompile(`(?m)^\s+` + name + `\s+D`).MatchString(out) }
	if !ok || listed("privat") || !listed("lesen") {
		e.t.Errorf("berta's list of Team (privat must be hidden, lesen listed):\n%s", out)
	}
	text += e.can("Team", "berta", pwBerta, `get lesen\doc.txt /tmp/doc.txt; put `+localFile+` b.txt`)
	text += e.cannot("Team", "berta", pwBerta, `put `+localFile+` lesen\b.txt`, "NT_STATUS_ACCESS_DENIED")
	text += e.cannot("Team", "carl", pwCarl, "ls", "tree connect failed: NT_STATUS_ACCESS_DENIED")
	block("acl-access", text)
}

// teamRestore: anna deletes privat/tief/f.txt and its folder; restored through the API, privat/tief is made again as the
// owner of the share's folder (0775 and the default ACL of the conversion) with privat's rights: anna reads the file,
// berta may not list the folder.
func (e *e2e) teamRestore() {
	text := e.can("Team", "anna", pwAnna, `mkdir privat\tief; put `+localFile+` privat\tief\f.txt; del privat\tief\f.txt; rmdir privat\tief`)
	if exists(team + "/privat/tief") {
		e.t.Fatal("privat/tief is still there")
	}
	e.change(actions.Request{Kind: "recycle-restore", Name: "Team", Item: "privat/tief/f.txt"})
	uid, gid, _, _ := statOf(team + "/privat/tief")
	if uid != 1000 || gid != 1000 {
		e.t.Errorf("privat/tief: %d:%d, want the share folder's 1000:1000", uid, gid)
	}
	e.wantMode(team+"/privat/tief", 0o775)
	root, err := safefs.Open("/")
	if err != nil {
		e.t.Fatal(err)
	}
	defer root.Close()
	rights, mixed, partial, err := acl.Read(root, team, "privat/tief")
	if want := map[string]samba.Right{e.sids()["anna"]: samba.Write}; err != nil || mixed || partial || !maps.Equal(rights, want) {
		e.t.Errorf("the rights of privat/tief: %v (mixed %v, partial %v, %v), want %v", rights, mixed, partial, err, want)
	}
	text += e.can("Team", "anna", pwAnna, `get privat\tief\f.txt /tmp/f.txt`)
	text += e.cannot("Team", "berta", pwBerta, `ls privat\tief\*`, "NT_STATUS_ACCESS_DENIED")
	block("acl-restore", text+modeLine(team+"/privat/tief")+xattrs(team+"/privat/tief"))
}
