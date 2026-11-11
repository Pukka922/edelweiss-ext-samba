package etcfile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// With ETCFILE_HOLD_LOCK set, the test binary is a helper that holds an fcntl lock on that file, the way lckpwdf(3)
// of the shadow tools does, until it is killed. With ETCFILE_TRY_LOCK set, it tries that lock once and exits with 0
// (got it) or 1 (refused).
func TestMain(m *testing.M) {
	if p := os.Getenv("ETCFILE_HOLD_LOCK"); p != "" {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			os.Exit(2)
		}
		lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
		if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk); err != nil {
			os.Exit(3)
		}
		fmt.Println("locked")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if p := os.Getenv("ETCFILE_TRY_LOCK"); p != "" {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			os.Exit(2)
		}
		lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
		if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const passwd = "root:x:0:0:root:/root:/bin/ash\ntom:x:1002:1002::/home/tom:/bin/ash\n"
const group = "root:x:0:root\ntom:x:1002:\n"

func etcRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "etc/passwd", passwd, 0o644)
	write(t, root, "etc/group", group, 0o644)
	return root
}

func write(t *testing.T, root, rel, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, rel), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, rel))
	return err == nil
}

// noLeftovers checks that none of the files an Apply uses for rel is left: its + file, its .lock and its .<pid>.
func noLeftovers(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		for _, f := range []string{rel + "+", rel + ".lock", rel + "." + strconv.Itoa(os.Getpid())} {
			if exists(root, f) {
				t.Errorf("%s is left behind", f)
			}
		}
	}
}

func shell(name, value string) Change {
	return Change{Path: "etc/passwd", Edit: func(old []byte) ([]byte, error) {
		b, _ := SetField(old, name, 6, value)
		return b, nil
	}}
}

func renameIn(path string) Change {
	return Change{Path: path, Edit: func(old []byte) ([]byte, error) {
		b, _ := Rename(old, "tom", "thomas")
		return b, nil
	}}
}

func fast(root string) Writer {
	return Writer{Root: root, Tries: 3, Wait: time.Millisecond, LockTimeout: 50 * time.Millisecond}
}

// patient waits long enough for the goroutines of the tests that take 20 ms to do their part.
func patient(root string) Writer {
	return Writer{Root: root, Tries: 200, Wait: 2 * time.Millisecond, LockTimeout: time.Second}
}

func TestApplyChangesOnlyTheEntry(t *testing.T) {
	root := etcRoot(t)
	if err := fast(root).Apply(shell("tom", "/sbin/nologin")); err != nil {
		t.Fatal(err)
	}
	want := "root:x:0:0:root:/root:/bin/ash\ntom:x:1002:1002::/home/tom:/sbin/nologin\n"
	if got := read(t, root, "etc/passwd"); got != want {
		t.Fatalf("passwd = %q, want %q", got, want)
	}
	if got := read(t, root, "etc/passwd-"); got != passwd {
		t.Fatalf("backup = %q, want the old content", got)
	}
	noLeftovers(t, root, "etc/passwd")
}

func TestApplyKeepsTheMode(t *testing.T) {
	root := etcRoot(t)
	write(t, root, "etc/shadow", "tom:!:19000:0:99999:7:::\n", 0o640)
	// As a normal user, only a group of the user can be the group of the old file: that has to survive too.
	wantGid := os.Getgid()
	if groups, err := os.Getgroups(); err == nil {
		for _, g := range groups {
			if g == os.Getgid() {
				continue
			}
			if err := os.Chown(filepath.Join(root, "etc/shadow"), -1, g); err == nil {
				wantGid = g
			}
			break
		}
	}
	if wantGid == os.Getgid() {
		t.Log("no supplementary group to change the group of the old file to: only the owner and the mode are checked")
	}
	err := fast(root).Apply(renameIn("etc/shadow"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "etc/shadow"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v, want 0640", fi.Mode().Perm())
	}
	st := fi.Sys().(*syscall.Stat_t)
	if int(st.Uid) != os.Getuid() {
		t.Fatalf("owner %d, want %d", st.Uid, os.Getuid())
	}
	if int(st.Gid) != wantGid {
		t.Fatalf("group %d, want %d", st.Gid, wantGid)
	}
	if got := read(t, root, "etc/shadow"); got != "thomas:!:19000:0:99999:7:::\n" {
		t.Fatalf("shadow = %q", got)
	}
}

// A <file>+ of another program (busybox, or a crash) is its lock: Apply waits, then gives up with ErrBusy, changes
// nothing and leaves the foreign file where it is.
func TestApplyBusyLeavesForeignPlusFile(t *testing.T) {
	root := etcRoot(t)
	write(t, root, "etc/group+", "foreign", 0o600)
	err := fast(root).Apply(shell("tom", "/sbin/nologin"), renameIn("etc/group"))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Apply = %v, want ErrBusy", err)
	}
	if got := read(t, root, "etc/passwd"); got != passwd {
		t.Fatalf("passwd changed: %q", got)
	}
	if got := read(t, root, "etc/group+"); got != "foreign" {
		t.Fatalf("the foreign group+ is %q", got)
	}
	noLeftovers(t, root, "etc/passwd")
	if exists(root, "etc/group.lock") {
		t.Fatal("etc/group.lock is left behind")
	}
}

func TestApplyWaitsForAPlusFileToGo(t *testing.T) {
	root := etcRoot(t)
	write(t, root, "etc/passwd+", "foreign", 0o600)
	go func() {
		time.Sleep(20 * time.Millisecond)
		os.Remove(filepath.Join(root, "etc/passwd+"))
	}()
	if err := patient(root).Apply(shell("tom", "/sbin/nologin")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "etc/passwd"); got == passwd {
		t.Fatal("passwd unchanged")
	}
	noLeftovers(t, root, "etc/passwd")
}

// The + file is the lock of busybox, so the old file is read only after it is taken: busybox, which holds passwd+ with
// a new line for anna, installs it while Apply waits, and the change of tom has to be made on that file, not on the one
// Apply would have read before.
func TestApplyReadsTheFileOnlyAfterTakingThePlusFile(t *testing.T) {
	root := etcRoot(t)
	anna := "anna:x:1001:1001::/home/anna:/bin/ash\n"
	write(t, root, "etc/passwd+", passwd+anna, 0o600)
	done := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		done <- os.Rename(filepath.Join(root, "etc/passwd+"), filepath.Join(root, "etc/passwd"))
	}()
	err := patient(root).Apply(shell("tom", "/sbin/nologin"))
	if rerr := <-done; rerr != nil {
		t.Fatal(rerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	want := "root:x:0:0:root:/root:/bin/ash\ntom:x:1002:1002::/home/tom:/sbin/nologin\n" + anna
	if got := read(t, root, "etc/passwd"); got != want {
		t.Fatalf("passwd = %q, want %q", got, want)
	}
	noLeftovers(t, root, "etc/passwd")
}

func TestApplyWaitsForPwdLock(t *testing.T) {
	root := etcRoot(t)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "ETCFILE_HOLD_LOCK="+filepath.Join(root, PwdLock))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "locked\n" {
		t.Fatalf("the helper said %q", line)
	}
	err = fast(root).Apply(shell("tom", "/sbin/nologin"))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Apply = %v, want ErrBusy", err)
	}
	if got := read(t, root, "etc/passwd"); got != passwd {
		t.Fatalf("passwd changed: %q", got)
	}
	noLeftovers(t, root, "etc/passwd")
}

// tryPwdLock asks a helper process whether it can take the lckpwdf lock now: 0 got it, 1 refused.
func tryPwdLock(t *testing.T, root string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	// GORACE: a helper built with -race would otherwise sleep a second before it exits
	cmd.Env = append(os.Environ(), "ETCFILE_TRY_LOCK="+filepath.Join(root, PwdLock), "GORACE=atexit_sleep_ms=0")
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return ee.ExitCode()
}

// While the files are written, the lckpwdf lock, the shadow lock <file>.lock (with the PID in it) and the + file of
// the file are held; afterwards none of them is.
func TestApplyHoldsTheLocksWhileWriting(t *testing.T) {
	root := etcRoot(t)
	called := false
	err := fast(root).Apply(Change{Path: "etc/passwd", Edit: func(old []byte) ([]byte, error) {
		called = true
		if got := tryPwdLock(t, root); got != 1 {
			t.Errorf("the helper got the lock of %s (%d) while Apply holds it", PwdLock, got)
		}
		if got := read(t, root, "etc/passwd.lock"); got != strconv.Itoa(os.Getpid()) {
			t.Errorf("etc/passwd.lock holds %q, want the PID", got)
		}
		if !exists(root, "etc/passwd+") {
			t.Error("etc/passwd+ does not exist while the file is edited")
		}
		b, _ := SetField(old, "tom", 6, "/sbin/nologin")
		return b, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Edit was not called")
	}
	if got := tryPwdLock(t, root); got != 0 {
		t.Fatalf("the helper cannot get the lock of %s after Apply (%d)", PwdLock, got)
	}
	noLeftovers(t, root, "etc/passwd")
}

// On Alpine (musl) lckpwdf is a stub, and the shadow tools lock a file with <file>.lock, a link to <file>.<pid>.
// A <file>.lock of another program is that lock: Apply waits, then gives up with ErrBusy, changes nothing, never
// takes the + file and leaves the foreign lock where it is.
func TestApplyBusyLeavesForeignShadowLock(t *testing.T) {
	for _, locked := range []string{"etc/passwd", "etc/group"} {
		t.Run(locked, func(t *testing.T) {
			root := etcRoot(t)
			write(t, root, locked+".lock", "4242", 0o600)
			err := fast(root).Apply(shell("tom", "/sbin/nologin"), renameIn("etc/group"))
			if !errors.Is(err, ErrBusy) {
				t.Fatalf("Apply = %v, want ErrBusy", err)
			}
			if got := read(t, root, "etc/passwd"); got != passwd {
				t.Fatalf("passwd changed: %q", got)
			}
			if got := read(t, root, "etc/group"); got != group {
				t.Fatalf("group changed: %q", got)
			}
			if got := read(t, root, locked+".lock"); got != "4242" {
				t.Fatalf("the foreign %s.lock is %q", locked, got)
			}
			// the foreign lock itself is the one file that has to stay
			if err := os.Remove(filepath.Join(root, locked+".lock")); err != nil {
				t.Fatal(err)
			}
			noLeftovers(t, root, "etc/passwd", "etc/group")
		})
	}
}

func TestApplyWaitsForAShadowLockToGo(t *testing.T) {
	root := etcRoot(t)
	write(t, root, "etc/passwd.lock", "4242", 0o600)
	go func() {
		time.Sleep(20 * time.Millisecond)
		os.Remove(filepath.Join(root, "etc/passwd.lock"))
	}()
	if err := patient(root).Apply(shell("tom", "/sbin/nologin")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "etc/passwd"); got == passwd {
		t.Fatal("passwd unchanged")
	}
	noLeftovers(t, root, "etc/passwd")
}

// An error before the first file is replaced changes nothing and removes the + files of this call.
func TestApplyErrorBeforeCommitChangesNothing(t *testing.T) {
	root := etcRoot(t)
	boom := errors.New("boom")
	err := fast(root).Apply(shell("tom", "/sbin/nologin"), Change{Path: "etc/group",
		Edit: func([]byte) ([]byte, error) { return nil, boom }})
	if !errors.Is(err, boom) {
		t.Fatalf("Apply = %v, want boom", err)
	}
	if got := read(t, root, "etc/passwd"); got != passwd {
		t.Fatalf("passwd changed: %q", got)
	}
	noLeftovers(t, root, "etc/passwd", "etc/group")
	if exists(root, "etc/passwd-") {
		t.Fatal("etc/passwd- exists")
	}
}

func TestApplyMissingFile(t *testing.T) {
	root := etcRoot(t)
	gshadow := func(optional bool) Change {
		return Change{Path: "etc/gshadow", Optional: optional, Edit: func(old []byte) ([]byte, error) { return old, nil }}
	}
	if err := fast(root).Apply(shell("tom", "/sbin/nologin"), gshadow(true)); err != nil {
		t.Fatalf("an optional missing file: %v", err)
	}
	if err := fast(root).Apply(gshadow(false)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file: %v, want ErrNotExist", err)
	}
	noLeftovers(t, root, "etc/gshadow", "etc/passwd")
}

// A change that changes nothing does not rewrite the file.
func TestApplyUnchangedKeepsTheFile(t *testing.T) {
	root := etcRoot(t)
	before, err := os.Stat(filepath.Join(root, "etc/passwd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fast(root).Apply(shell("tom", "/bin/ash")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(root, "etc/passwd"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("passwd was replaced although nothing changed")
	}
	if exists(root, "etc/passwd-") {
		t.Fatal("a backup was made")
	}
	noLeftovers(t, root, "etc/passwd")
}

func TestApplySeveralFiles(t *testing.T) {
	root := etcRoot(t)
	err := fast(root).Apply(renameIn("etc/passwd"), renameIn("etc/group"))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "etc/passwd"); got != "root:x:0:0:root:/root:/bin/ash\nthomas:x:1002:1002::/home/tom:/bin/ash\n" {
		t.Fatalf("passwd = %q", got)
	}
	if got := read(t, root, "etc/group"); got != "root:x:0:root\nthomas:x:1002:\n" {
		t.Fatalf("group = %q", got)
	}
	noLeftovers(t, root, "etc/passwd", "etc/group")
}

// busybox follows a symbolic link: the target is changed, locked (target+) and backed up, and the link stays a link.
func TestApplyFollowsASymlink(t *testing.T) {
	setup := func(t *testing.T) string {
		root := etcRoot(t)
		if err := os.Mkdir(filepath.Join(root, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, root, "data/passwd", passwd, 0o644)
		if err := os.Remove(filepath.Join(root, "etc/passwd")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../data/passwd", filepath.Join(root, "etc/passwd")); err != nil {
			t.Fatal(err)
		}
		return root
	}
	t.Run("changes the target", func(t *testing.T) {
		root := setup(t)
		if err := fast(root).Apply(shell("tom", "/sbin/nologin")); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(filepath.Join(root, "etc/passwd"))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("etc/passwd is not a symlink any more: %v, %v", fi, err)
		}
		want := "root:x:0:0:root:/root:/bin/ash\ntom:x:1002:1002::/home/tom:/sbin/nologin\n"
		if got := read(t, root, "data/passwd"); got != want {
			t.Fatalf("data/passwd = %q, want %q", got, want)
		}
		if got := read(t, root, "data/passwd-"); got != passwd {
			t.Fatalf("data/passwd- = %q, want the old content", got)
		}
		if exists(root, "etc/passwd-") {
			t.Fatal("the backup is next to the link")
		}
		noLeftovers(t, root, "etc/passwd", "data/passwd")
	})
	t.Run("the + file of the target is the lock", func(t *testing.T) {
		root := setup(t)
		write(t, root, "data/passwd+", "foreign", 0o600)
		if err := fast(root).Apply(shell("tom", "/sbin/nologin")); !errors.Is(err, ErrBusy) {
			t.Fatalf("Apply = %v, want ErrBusy", err)
		}
		if got := read(t, root, "data/passwd+"); got != "foreign" {
			t.Fatalf("the foreign data/passwd+ is %q", got)
		}
		if got := read(t, root, "data/passwd"); got != passwd {
			t.Fatalf("passwd changed: %q", got)
		}
	})
	t.Run("the shadow lock of the target is the lock", func(t *testing.T) {
		root := setup(t)
		write(t, root, "data/passwd.lock", "4242", 0o600)
		if err := fast(root).Apply(shell("tom", "/sbin/nologin")); !errors.Is(err, ErrBusy) {
			t.Fatalf("Apply = %v, want ErrBusy", err)
		}
		if got := read(t, root, "data/passwd.lock"); got != "4242" {
			t.Fatalf("the foreign data/passwd.lock is %q", got)
		}
	})
}

// replacePlus puts another file where the + file of rel is, as another program could while Apply works.
func replacePlus(t *testing.T, root, rel string) {
	t.Helper()
	write(t, root, "etc/intruder", "intruder", 0o600)
	if err := os.Rename(filepath.Join(root, "etc/intruder"), filepath.Join(root, rel+"+")); err != nil {
		t.Fatal(err)
	}
}

// A + file that is not the one Apply created is never installed and never removed.
func TestApplyLeavesAReplacedPlusFileAlone(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		root := etcRoot(t)
		replace := Change{Path: "etc/group", Edit: func(old []byte) ([]byte, error) {
			replacePlus(t, root, "etc/passwd") // passwd+ is ready by now
			b, _ := Rename(old, "tom", "thomas")
			return b, nil
		}}
		err := fast(root).Apply(shell("tom", "/sbin/nologin"), replace)
		if err == nil || !strings.Contains(err.Error(), "etc/passwd+ was replaced by another program") {
			t.Fatalf("Apply = %v, want the error that passwd+ was replaced", err)
		}
		var partial *PartialError
		if errors.As(err, &partial) {
			t.Fatalf("Apply = %v, nothing was replaced yet, so no PartialError", err)
		}
		if got := read(t, root, "etc/passwd"); got != passwd {
			t.Fatalf("passwd = %q, the foreign file was installed or passwd changed", got)
		}
		if got := read(t, root, "etc/group"); got != group {
			t.Fatalf("group changed: %q", got)
		}
		if got := read(t, root, "etc/passwd+"); got != "intruder" {
			t.Fatalf("the foreign passwd+ is %q", got)
		}
		noLeftovers(t, root, "etc/group")
		if exists(root, "etc/passwd.lock") {
			t.Fatal("etc/passwd.lock is left behind")
		}
	})
	t.Run("not removed", func(t *testing.T) {
		root := etcRoot(t)
		boom := errors.New("boom")
		fail := Change{Path: "etc/group", Edit: func([]byte) ([]byte, error) {
			replacePlus(t, root, "etc/passwd")
			return nil, boom
		}}
		if err := fast(root).Apply(shell("tom", "/sbin/nologin"), fail); !errors.Is(err, boom) {
			t.Fatalf("Apply = %v, want boom", err)
		}
		if got := read(t, root, "etc/passwd+"); got != "intruder" {
			t.Fatalf("the foreign passwd+ is %q", got)
		}
		if got := read(t, root, "etc/passwd"); got != passwd {
			t.Fatalf("passwd changed: %q", got)
		}
		noLeftovers(t, root, "etc/group")
		if exists(root, "etc/passwd.lock") {
			t.Fatal("etc/passwd.lock is left behind")
		}
	})
}

// fcntl locks belong to the process, so a second Apply of this process would get the lckpwdf lock at once: it has to
// wait for the first instead (here with so few tries that it fails if it does not).
func TestApplyOneAtATimeInTheProcess(t *testing.T) {
	root := etcRoot(t)
	second := make(chan error, 1)
	first := Change{Path: "etc/passwd", Edit: func(old []byte) ([]byte, error) {
		go func() { second <- fast(root).Apply(renameIn("etc/passwd")) }()
		time.Sleep(30 * time.Millisecond)
		b, _ := SetField(old, "tom", 6, "/sbin/nologin")
		return b, nil
	}}
	if err := fast(root).Apply(first); err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatalf("the second Apply: %v", err)
	}
	want := "root:x:0:0:root:/root:/bin/ash\nthomas:x:1002:1002::/home/tom:/sbin/nologin\n"
	if got := read(t, root, "etc/passwd"); got != want {
		t.Fatalf("passwd = %q, want %q", got, want)
	}
	noLeftovers(t, root, "etc/passwd")
}

// A failure after the first file was replaced is a PartialError that names the files done, and removes the rest.
func TestApplyPartialError(t *testing.T) {
	root := etcRoot(t)
	// the rename of group+ over a directory fails, after passwd was replaced
	breakGroup := Change{Path: "etc/group", Edit: func(old []byte) ([]byte, error) {
		g := filepath.Join(root, "etc/group")
		if err := os.Remove(g); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(g, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := Rename(old, "tom", "thomas")
		return b, nil
	}}
	err := fast(root).Apply(shell("tom", "/sbin/nologin"), breakGroup)
	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("Apply = %v, want a *PartialError", err)
	}
	if len(partial.Done) != 1 || partial.Done[0] != "etc/passwd" {
		t.Fatalf("Done = %v, want [etc/passwd]", partial.Done)
	}
	if partial.Err == nil || !errors.Is(err, partial.Err) {
		t.Fatalf("the PartialError does not wrap its cause: %v", err)
	}
	want := "root:x:0:0:root:/root:/bin/ash\ntom:x:1002:1002::/home/tom:/sbin/nologin\n"
	if got := read(t, root, "etc/passwd"); got != want {
		t.Fatalf("passwd = %q, want %q", got, want)
	}
	noLeftovers(t, root, "etc/passwd", "etc/group")
	if fi, err := os.Stat(filepath.Join(root, "etc/group")); err != nil || !fi.IsDir() {
		t.Fatalf("etc/group is not the directory of the test: %v, %v", fi, err)
	}
}

// An account file is never emptied by a change: that is a bug of the caller, and a login-less machine for the user.
func TestApplyRefusesEmptyContent(t *testing.T) {
	root := etcRoot(t)
	empty := Change{Path: "etc/group", Edit: func([]byte) ([]byte, error) { return nil, nil }}
	err := fast(root).Apply(shell("tom", "/sbin/nologin"), empty)
	if err == nil || !strings.Contains(err.Error(), "the new content of etc/group is empty") {
		t.Fatalf("Apply = %v, want the error that the new content of etc/group is empty", err)
	}
	if got := read(t, root, "etc/passwd"); got != passwd {
		t.Fatalf("passwd changed: %q", got)
	}
	if got := read(t, root, "etc/group"); got != group {
		t.Fatalf("group changed: %q", got)
	}
	noLeftovers(t, root, "etc/passwd", "etc/group")
}

// An empty file that stays empty is not an error.
func TestApplyEmptyFileStaysEmpty(t *testing.T) {
	root := etcRoot(t)
	write(t, root, "etc/gshadow", "", 0o640)
	keep := Change{Path: "etc/gshadow", Edit: func(old []byte) ([]byte, error) { return old, nil }}
	if err := fast(root).Apply(keep); err != nil {
		t.Fatal(err)
	}
	noLeftovers(t, root, "etc/gshadow")
}
