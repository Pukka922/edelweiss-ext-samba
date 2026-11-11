package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-crypt/crypt/algorithm/shacrypt"
	"github.com/gofiber/fiber/v3"
	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/etcfile"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss/sdk"
	"github.com/Pukka922/edelweiss/sdk/sdktest"
)

// The tests write a user.* attribute instead of security.NTACL: only CAP_SYS_ADMIN may write security.*.
func TestMain(m *testing.M) {
	acl.Attr = "user.NTACL"
	os.Exit(m.Run())
}

var (
	admin = sdk.User{Name: "myUser", UID: 1000, Admin: true, Lang: "en"}
	plain = sdk.User{Name: "myUser", UID: 1000, Admin: false, Lang: "en"}
)

// now is the time of the API in the tests.
var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

const (
	binary    = "/usr/local/bin/edelweiss-ext-samba"
	self      = "/proc/self/exe"
	pdbedit   = "/usr/bin/pdbedit"
	smbpasswd = "/usr/bin/smbpasswd"
	smbstatus = "/usr/bin/smbstatus"
	testparm  = "/usr/bin/testparm"
	lbu       = "/usr/sbin/lbu"
	sidBase   = "S-1-5-21-1-2-3-"
)

// benPassword is the Linux password of the host user ben.
const benPassword = "richtig"

var (
	hashOnce sync.Once
	benHash  string
)

// benShadowHash is a sha512-crypt hash of benPassword (made once: hashing is slow).
func benShadowHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		h, err := shacrypt.NewSHA512()
		if err != nil {
			t.Fatal(err)
		}
		d, err := h.Hash(benPassword)
		if err != nil {
			t.Fatal(err)
		}
		benHash = d.Encode()
	})
	return benHash
}

// harness is the API on a test host below a temp root:
//   - / on sda1 and the data disk /mnt/disk (ext4) with the folders data, data/sub and more, owned by the user of the
//     test as myUser:myGroup;
//   - the pure Samba user anna and the host user ben (password benPassword), both with a Samba entry;
//   - Samba set up with the share Fotos of data (anna writes, ben reads), its services started.
//
// The fakes record what the API runs (host), what the jobs run (cmds), and the applets of busybox (bb), which change the
// account files as busybox does; order has what each ran, in order.
type harness struct {
	t        *testing.T
	root     string
	app      *fiber.App
	api      *API
	host     *run.Fake
	cmds     *run.Fake
	bb       *busybox.Fake
	launcher *launcher
	logged   func() string
	fsid     string

	mu      sync.Mutex
	passdb  map[string]string // the Samba users by name, with their SIDs
	nextRID int
	order   []string
	status  string // what smbstatus --json prints
	jobFail func(run.Cmd) error
	// what testparm -s /etc/samba/smb.conf and pdbedit -L print of a Samba that was there before the setup
	foreignConf, foreignUsers string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	f := &harness{t: t, root: t.TempDir(), passdb: map[string]string{"anna": sidBase + "1000", "ben": sidBase + "1001"}, nextRID: 1002}
	f.logged = captureLogs(t)
	f.write("/proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n")
	for _, d := range []string{"/mnt/disk/data/sub", "/mnt/disk/more", "/etc/samba"} {
		f.mkdir(d)
	}
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	f.write(accounts.PasswdFile, "root:x:0:0:root:/root:/bin/ash\nsshd:x:22:22::/var/empty:/sbin/nologin\n"+
		"myUser:x:"+uid+":"+gid+"::/home/myUser:/bin/ash\nanna:x:3000:103::/home/anna:/sbin/nologin\n"+
		"ben:x:3001:3001::/home/ben:/bin/ash\n")
	f.write(accounts.GroupFile, "root:x:0:root\nedelweiss-samba:x:103:anna\nmyGroup:x:"+gid+":\nben:x:3001:\n")
	f.file(accounts.ShadowFile, "root:*::0:::::\nsshd:!::0:::::\nmyUser:!:20761:0:99999:7:::\nanna:!:20761:0:99999:7:::\n"+
		"ben:"+benShadowHash(t)+":20761:0:99999:7:::\n", 0o640)
	for _, tool := range []string{pdbedit, smbpasswd, smbstatus, testparm, "/usr/bin/sharesec", "/usr/bin/smbcontrol",
		"/usr/sbin/smbd", lbu, "/bin/rm", "/bin/busybox", "/sbin/rc-service", "/sbin/rc-update"} {
		f.file(tool, "", 0o755)
	}
	f.write(host.InstalledDB, "P:samba\n\nP:avahi\n\nP:dbus\n\nP:wsdd\n\n")
	f.write("/etc/conf.d/samba", "daemon_list=\"smbd\"\n")
	f.write(samba.ConfPath, "# Made by edelweiss-ext-samba. Changes are overwritten.\n")
	f.services("dbus", "avahi-daemon", "samba", "wsdd", "crond")
	fsid, err := paths.FsidOf(host.FS{Root: f.root}, "/mnt/disk")
	if err != nil {
		t.Fatal(err)
	}
	f.fsid = fsid
	f.config(f.fotos())

	f.host = &run.Fake{OnRun: f.hostCmd}
	f.cmds = &run.Fake{OnRun: f.jobCmd}
	f.bb = &busybox.Fake{OnRun: f.applet}
	store := jobs.Store{Base: f.path("/run/edelweiss-ext-samba/jobs"), LockPath: f.path("/run/edelweiss-ext-samba/lock")}
	f.api = &API{
		Root: f.root, Runner: f.host, Jobs: store, Self: self, Binary: binary,
		Accounts: &accounts.Service{Root: f.root, Busybox: f.bb,
			Writer: etcfile.Writer{Root: f.root, Tries: 3, Wait: time.Millisecond, LockTimeout: 50 * time.Millisecond},
			Lock:   accounts.Lock{Path: f.path("/run/edelweiss-ext-users.lock"), Wait: 100 * time.Millisecond}},
		Ifaces: func() ([]samba.Iface, error) {
			return []samba.Iface{
				{Name: "docker0", Bridge: true, Addrs: []samba.Addr{{Iface: "docker0", CIDR: "172.17.0.1/16", Private: true}}},
				{Name: "eth0", Addrs: []samba.Addr{{Iface: "eth0", CIDR: "192.168.1.10/24", Private: true}}},
			}, nil
		},
		Statfs: func(string) (uint64, uint64, error) { return 1000, 400, nil },
		Now:    func() time.Time { return now },
		Poll:   5 * time.Millisecond,
	}
	f.launcher = &launcher{t: t, cfg: jobs.RunnerConfig{Store: store, Now: time.Now, Poll: 10 * time.Millisecond,
		Exec: jobs.Executor{Root: f.root, Runner: f.cmds}}}
	f.api.Launcher = f.launcher
	t.Cleanup(f.launcher.wg.Wait) // a job that still runs must not write into a removed temp dir
	f.app = fiber.New(fiber.Config{ErrorHandler: sdk.ErrorHandler})
	Register(f.app, f.api)
	return f
}

// fotos is the share Fotos of the folder data: anna writes, ben reads.
func (f *harness) fotos() samba.Share {
	return samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Mount: "/mnt/disk", Fsid: f.fsid,
		Rights: map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}}
}

// config writes the config of the extension with these shares.
func (f *harness) config(shares ...samba.Share) {
	f.t.Helper()
	cfg := samba.Config{Version: samba.ConfigVersion, Settings: samba.Settings{Discovery: true, Interfaces: []string{"eth0"}},
		Shares: shares}
	f.write(samba.ConfigPath, string(cfg.JSON()))
}

// loaded is the config of the extension as it is on the disk.
func (f *harness) loaded() samba.Config {
	f.t.Helper()
	cfg, _, err := samba.Load(host.FS{Root: f.root})
	if err != nil {
		f.t.Fatal(err)
	}
	return cfg
}

// diskless makes the host run from RAM: lbu commit saves what changed.
func (f *harness) diskless() {
	f.write("/proc/mounts", "tmpfs / tmpfs rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n")
}

// services puts the services into the runlevel and has OpenRC show them started.
func (f *harness) services(svcs ...string) {
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

// log adds what ran to order.
func (f *harness) log(who, what string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, who+": "+what)
}

func (f *harness) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.order)
}

// samba sets the Samba users and their SIDs.
func (f *harness) samba(users map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passdb = maps.Clone(users)
}

func (f *harness) sambaUsers() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.passdb)
}

// hostCmd is what the commands of the API do: pdbedit and smbpasswd on the Samba users of passdb, smbstatus, lbu.
func (f *harness) hostCmd(c run.Cmd) (string, error) {
	f.log("host", c.String())
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case c.Path == pdbedit && slices.Equal(c.Args, []string{"-L", "-v"}):
		var b strings.Builder
		for _, name := range slices.Sorted(maps.Keys(f.passdb)) {
			fmt.Fprintf(&b, "---------------\nUnix username:        %s\nAccount Flags:        [U          ]\nUser SID:             %s\n", name, f.passdb[name])
		}
		return b.String(), nil
	case c.Path == pdbedit && len(c.Args) == 4 && c.Args[0] == "-a":
		if _, ok := f.passdb[c.Args[2]]; !ok {
			f.passdb[c.Args[2]] = sidBase + strconv.Itoa(f.nextRID)
			f.nextRID++
		}
	case c.Path == pdbedit && len(c.Args) == 2 && c.Args[0] == "-x":
		delete(f.passdb, c.Args[1])
	case c.Path == smbstatus:
		return f.status, nil
	case c.Path == testparm && slices.Equal(c.Args, []string{"-s", samba.ConfPath}):
		return f.foreignConf, nil
	case c.Path == pdbedit && slices.Equal(c.Args, []string{"-L"}):
		return f.foreignUsers, nil
	}
	return "", nil
}

// jobCmd is what the commands of the jobs do: nothing, or the failure of jobFail.
func (f *harness) jobCmd(c run.Cmd) (string, error) {
	f.log("job", c.String())
	f.mu.Lock()
	fail := f.jobFail
	f.mu.Unlock()
	if fail != nil {
		return "", fail(c)
	}
	return "", nil
}

// applet is what busybox does to the account files, as the applets of Alpine do.
func (f *harness) applet(args []string) error {
	f.log("busybox", strings.Join(args, " "))
	var flags, rest []string
	opts := map[string]string{}
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-s" || a == "-u" || a == "-G" || a == "-g":
			i++
			opts[a] = args[i]
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		default:
			rest = append(rest, a)
		}
	}
	system := slices.Contains(flags, "-S")
	groups := accounts.ParseGroup(f.read(accounts.GroupFile))
	nextID := func(used func(int) bool) int {
		id := 3100
		if system {
			id = 100
		}
		for used(id) {
			id++
		}
		return id
	}
	switch args[0] {
	case "addgroup":
		if len(rest) == 2 { // addgroup <user> <group>
			return f.member(rest[1], rest[0])
		}
		gid, _ := strconv.Atoi(opts["-g"])
		if gid == 0 {
			gid = nextID(func(id int) bool {
				return slices.ContainsFunc(groups, func(g accounts.GroupEntry) bool { return g.GID == id })
			})
		}
		f.write(accounts.GroupFile, f.read(accounts.GroupFile)+fmt.Sprintf("%s:x:%d:\n", rest[0], gid))
	case "adduser":
		name := rest[0]
		gid := -1
		for _, g := range groups {
			if g.Name == opts["-G"] {
				gid = g.GID
			}
		}
		if gid < 0 {
			return errors.New("no group " + opts["-G"])
		}
		pw := accounts.ParsePasswd(f.read(accounts.PasswdFile))
		uid, _ := strconv.Atoi(opts["-u"])
		if uid == 0 {
			uid = nextID(func(id int) bool {
				return slices.ContainsFunc(pw, func(e accounts.PasswdEntry) bool { return e.UID == id })
			})
		}
		f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+fmt.Sprintf("%s:x:%d:%d::/home/%s:%s\n", name, uid, gid, name, opts["-s"]))
		f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+name+":!:20761:0:99999:7:::\n", 0o640)
		return f.member(opts["-G"], name)
	default:
		return errors.New("unexpected applet " + args[0])
	}
	return nil
}

// member lists user in the members of group, as busybox addgroup <user> <group> and adduser -G do.
func (f *harness) member(group, user string) error {
	for _, g := range accounts.ParseGroup(f.read(accounts.GroupFile)) {
		if g.Name == group {
			out, _ := etcfile.SetField([]byte(f.read(accounts.GroupFile)), group, 3, strings.Join(append(g.Members, user), ","))
			f.write(accounts.GroupFile, string(out))
			return nil
		}
	}
	return errors.New("no group " + group)
}

// line is the entry called name of the account file rel, "" when there is none.
func (f *harness) line(rel, name string) string {
	f.t.Helper()
	for _, l := range strings.Split(f.read(rel), "\n") {
		if strings.HasPrefix(l, name+":") {
			return l
		}
	}
	return ""
}

// path is p, a path of the test host, below the root.
func (f *harness) path(p string) string { return filepath.Join(f.root, p) }

func (f *harness) file(p, content string, mode os.FileMode) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.path(p)), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.path(p), []byte(content), mode); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(f.path(p), mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *harness) write(p, content string) { f.t.Helper(); f.file(p, content, 0o644) }

func (f *harness) read(p string) string {
	f.t.Helper()
	b, err := os.ReadFile(f.path(p))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *harness) mkdir(p string) {
	f.t.Helper()
	if err := os.MkdirAll(f.path(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func uidString() string { return strconv.Itoa(os.Getuid()) }

// specs are the files of the jobs (spec.json, state.json, the log), as they are written.
func (f *harness) specs() []string {
	f.t.Helper()
	files, err := filepath.Glob(f.path("/run/edelweiss-ext-samba/jobs/*/*"))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, string(b))
	}
	return out
}

// launcher is the Launcher of the tests: it hands the lock on as the real one does (a new descriptor of the same open
// file description) and runs the runner in this process, whose pid it hands back.
type launcher struct {
	t    *testing.T
	cfg  jobs.RunnerConfig
	err  error // when set, Launch fails with it
	dead bool  // when set, Launch runs no runner and hands back the pid of a process that has ended: a runner that died
	mu   sync.Mutex
	dirs []string
	wg   sync.WaitGroup
}

func (l *launcher) Launch(dir string, lock *os.File) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if l.dead {
		gone := exec.Command("/bin/true")
		if err := gone.Run(); err != nil {
			return 0, err
		}
		return gone.ProcessState.Pid(), nil
	}
	fd, err := unix.Dup(int(lock.Fd()))
	if err != nil {
		return 0, err
	}
	handed := os.NewFile(uintptr(fd), lock.Name())
	l.mu.Lock()
	l.dirs = append(l.dirs, dir)
	l.mu.Unlock()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer handed.Close()
		if code := jobs.RunJob(l.cfg, dir, handed); code != 0 {
			l.t.Errorf("RunJob = %d; want 0", code)
		}
	}()
	return os.Getpid(), nil
}

func (l *launcher) launched() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.dirs)
}

// answer is a response read to its end.
type answer struct {
	status int
	raw    string
}

func (f *harness) send(method, path string, body any, u sdk.User) answer {
	f.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	case []byte:
		r = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	return f.do(sdktest.Request(method, path, r, u))
}

func (f *harness) do(req *http.Request) answer {
	f.t.Helper()
	resp := sdktest.Do(f.t, f.app, req)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return answer{status: resp.StatusCode, raw: string(data)}
}

func (a answer) want(t *testing.T, status int) answer {
	t.Helper()
	if a.status != status {
		t.Fatalf("status %d (%s), want %d", a.status, a.raw, status)
	}
	return a
}

// wantError checks status and code of an error answer and returns its error object.
func (a answer) wantError(t *testing.T, status int, code string) map[string]any {
	t.Helper()
	a.want(t, status)
	var env struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(a.raw), &env); err != nil {
		t.Fatalf("answer %q: %v", a.raw, err)
	}
	if env.Error["code"] != code {
		t.Fatalf("error %v, want the code %s", env.Error, code)
	}
	if msg, _ := env.Error["message"].(string); msg == "" {
		t.Errorf("error %v has no message", env.Error)
	}
	return env.Error
}

func decodeAs[T any](t *testing.T, a answer) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(a.raw), &v); err != nil {
		t.Fatalf("answer %q: %v", a.raw, err)
	}
	return v
}

// captureLogs keeps what the API logs (and the SDK logs of the answers that are 500) out of the output of the tests,
// and returns what was logged so far.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	w := &logWriter{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return w.String
}

type logWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *logWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
