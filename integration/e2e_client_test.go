//go:build integration

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/api"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss/sdk"
	"github.com/Pukka922/edelweiss/sdk/sdktest"
)

// The extension in the container: its binary, which scripts/test-alpine.sh builds next to the test binary (the API starts
// the runner of a job from it, the steps of a job run it as --samba-tool, and smbd's root preexec and the periodic script
// of the recycle bins call it), and the data disk, a directory of this machine that the script mounts at /srv/data: a
// real mount of a file system with attributes, as the disk of a NAS.
const (
	extBinary = "/t/edelweiss-ext-samba"
	dataDisk  = "/srv/data"
)

// The users of the end-to-end tests: ben is the host user with UID 1000 who owns the shared folders; anna and carl are
// pure Samba users.
const (
	pwAnna = "anna-pw-1"
	pwBen  = "ben-linux-1"
	pwCarl = "carl-pw-1"
)

// The local files that smbclient puts.
const (
	localFile  = "/tmp/local.txt"
	localFile2 = "/tmp/local2.txt"
)

// e2e is the extension's HTTP API in this process on the real host of the container, wired as routes.go wires it, with
// the extension's binary for its jobs; the capture runs the other commands. What the API logs goes to logs.
type e2e struct {
	*capture
	api  *api.API
	app  *fiber.App
	logs *lockedBuffer
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	if st, err := os.Stat(extBinary); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("the extension's binary %s is not there: scripts/test-alpine.sh builds it (%v)", extBinary, err)
	}
	logs := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	a := &api.API{Root: "/", Runner: run.Exec{Env: run.Env}, Jobs: jobs.DefaultStore(),
		Launcher: jobs.ExecLauncher{Binary: extBinary}, Self: extBinary, Binary: extBinary}
	app := fiber.New(fiber.Config{ErrorHandler: sdk.ErrorHandler})
	api.Register(app, a)
	writeFile(t, localFile, "local\n")
	writeFile(t, localFile2, "local 2\n")
	return &e2e{capture: newCapture(t), api: a, app: app, logs: logs}
}

// finish ends the log of the test with what the API logged.
func (e *e2e) finish() {
	block("api-log", e.logs.String())
	e.capture.finish()
}

// The users the API sees: in admin mode, and without.
var (
	adminUser = sdk.User{Name: "ben", UID: 1000, Admin: true, Lang: "de"}
	plainUser = sdk.User{Name: "ben", UID: 1000, Lang: "de"}
)

// answer is a response of the API, read to its end.
type answer struct {
	status int
	body   []byte
}

// call sends the request to the API as the user u; a body that is not nil goes as JSON. A request may take as long as
// the API waits for a job.
func (e *e2e) call(u sdk.User, method, target string, body any) answer {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	resp, err := e.app.Test(sdktest.Request(method, target, r, u), fiber.TestConfig{Timeout: 15 * time.Minute, FailOnTimeout: true})
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return answer{status: resp.StatusCode, body: data}
}

// send is call in admin mode.
func (e *e2e) send(method, target string, body any) answer {
	e.t.Helper()
	return e.call(adminUser, method, target, body)
}

// ok checks the status of the answer and decodes its body into v, when v is not nil.
func (e *e2e) ok(a answer, status int, v any) {
	e.t.Helper()
	if a.status != status {
		e.t.Fatalf("status %d (%s), want %d", a.status, a.body, status)
	}
	if v != nil {
		if err := json.Unmarshal(a.body, v); err != nil {
			e.t.Fatalf("answer %s: %v", a.body, err)
		}
	}
}

// refused checks that the answer is the SDK's error envelope with the status and the code, and returns the error object
// (code, message and the details).
func (e *e2e) refused(a answer, status int, code string) map[string]any {
	e.t.Helper()
	var env struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(a.body, &env); err != nil || a.status != status || env.Error["code"] != code {
		e.t.Fatalf("answer %d %s, want %d with the code %s", a.status, a.body, status, code)
	}
	return env.Error
}

// view is GET /view in admin mode.
func (e *e2e) view() api.ViewResponse {
	e.t.Helper()
	var v api.ViewResponse
	e.ok(e.send("GET", "/view", nil), 200, &v)
	return v
}

// shareView is the share of the view with the name.
func (e *e2e) shareView(name string) api.ShareView {
	e.t.Helper()
	for _, s := range e.view().Config.Shares {
		if s.Name == name {
			return s
		}
	}
	e.t.Fatalf("the view has no share %q", name)
	return api.ShareView{}
}

// config is the config of the extension on the disk.
func (e *e2e) config() samba.Config {
	e.t.Helper()
	cfg, _, err := samba.Load(host.FS{Root: "/"})
	if err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

// shareOf is the share of the config with the name.
func (e *e2e) shareOf(name string) samba.Share {
	e.t.Helper()
	for _, s := range e.config().Shares {
		if s.Name == name {
			return s
		}
	}
	e.t.Fatalf("the config has no share %q", name)
	return samba.Share{}
}

// Jobs.

// start asks for the change (POST /jobs); the API has to start it as a job.
func (e *e2e) start(req actions.Request) jobs.State {
	e.t.Helper()
	var st jobs.State
	e.ok(e.send("POST", "/jobs", req), 202, &st)
	return st
}

// await polls the job until it ends (15 minutes at most); seen, when not nil, gets every state that reads running.
func (e *e2e) await(id string, seen func(jobs.State)) jobs.State {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Minute)
	for {
		var st jobs.State
		e.ok(e.send("GET", "/jobs/"+id, nil), 200, &st)
		if st.Status != jobs.Running {
			return st
		}
		if seen != nil {
			seen(st)
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the job %s still runs after 15 minutes: %+v", id, st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// change starts the change and waits for its job, which has to end done; it returns the job as it ended.
func (e *e2e) change(req actions.Request) jobs.State {
	e.t.Helper()
	return e.done(e.start(req).ID)
}

// done waits for the job, which has to end done.
func (e *e2e) done(id string) jobs.State {
	e.t.Helper()
	st := e.await(id, nil)
	if st.Status != jobs.Done {
		e.t.Fatalf("the job %s (%s %s) ended %s: %s %s\n%s\n%s", id, st.Kind, st.Target, st.Status, st.Code, st.Error,
			st.Output, e.jobFiles(id))
	}
	return st
}

// refuse asks for the change, which the API has to refuse with the status and the code before any job; it returns the
// error object.
func (e *e2e) refuse(req actions.Request, status int, code string) map[string]any {
	e.t.Helper()
	return e.refused(e.send("POST", "/jobs", req), status, code)
}

// jobFiles are the files of the job's directory (spec, state, log), for a failure.
func (e *e2e) jobFiles(id string) string {
	dir := filepath.Join(jobs.DefaultStore().Base, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Sprintf("(the job directory: %v)", err)
	}
	text := ""
	for _, en := range entries {
		text += "--- " + en.Name() + "\n" + fileText(filepath.Join(dir, en.Name()))
	}
	return text
}

// jobTexts is everything in the job directories, all jobs: a password must never be in there.
func jobTexts(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(jobs.DefaultStore().Base, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, f := range files {
		text += fileText(f)
	}
	return text
}

// setupServices are the services that the setup starts on a host with a local interface: the discovery is on.
var setupServices = []string{"dbus", "avahi-daemon", "samba", "wsdd", "crond"}

// setup sets Samba up through the API, and waits until OpenRC has started every service of the setup (setupServices, by
// name: an empty view would not do) and smbd listens on the loopback address (wsdd 0.8 of Alpine 3.22 reads starting for
// a moment after samba starts).
func (e *e2e) setup() jobs.State {
	e.t.Helper()
	st := e.change(actions.Request{Kind: "setup"})
	var v api.ViewResponse
	if _, ok := poll(30*time.Second, 200*time.Millisecond, func() bool {
		v = e.view()
		return !slices.ContainsFunc(setupServices, func(s string) bool { return !v.Services[s] }) && listening("127.0.0.1:445")
	}); !ok {
		e.t.Fatalf("30 s after the setup: services %v, smbd listens: %v", v.Services, listening("127.0.0.1:445"))
	}
	for _, it := range v.Setup.Items {
		if !it.OK {
			e.t.Errorf("after the setup: %s is not OK (%s)", it.ID, it.Detail)
		}
	}
	return st
}

// settings changes the settings of the config with edit, through the API, and waits for the job.
func (e *e2e) settings(edit func(*samba.Settings)) jobs.State {
	e.t.Helper()
	s := e.config().Settings
	edit(&s)
	return e.change(actions.Request{Kind: "settings", Settings: &s})
}

// addShare adds the share through the API and waits for the job.
func (e *e2e) addShare(sh samba.Share) jobs.State {
	e.t.Helper()
	return e.change(actions.Request{Kind: "share-add", Share: &sh})
}

// editShare changes the share called name through the API: edit gets it as the config has it.
func (e *e2e) editShare(name string, edit func(*samba.Share)) jobs.State {
	e.t.Helper()
	sh := e.shareOf(name)
	edit(&sh)
	return e.change(actions.Request{Kind: "share-edit", Name: name, Share: &sh})
}

// Accounts.

// sambaUser adds the pure Samba user through the API.
func (e *e2e) sambaUser(name, pw string) {
	e.t.Helper()
	e.ok(e.send("POST", "/users/samba", map[string]string{"name": name, "password": pw}), 200, nil)
}

// linuxUser makes a host user with busybox, as an admin would: adduser with the UID and a home, and the Linux password
// with chpasswd (on stdin).
func (e *e2e) linuxUser(name string, uid int, pw string) {
	e.t.Helper()
	e.step(e.tool("busybox"), "adduser", "-D", "-u", fmt.Sprint(uid), "-s", "/bin/sh", name)
	e.stepIn(name+":"+pw+"\n", e.tool("busybox"), "chpasswd")
}

// hostUser gives the host user Samba access through the API, with the Linux password.
func (e *e2e) hostUser(name, pw string) {
	e.t.Helper()
	e.ok(e.send("POST", "/users/host", map[string]string{"name": name, "password": pw}), 200, nil)
}

// deleteUser takes the Samba access of the user through the API and waits for the job that drops the name from the
// config, when the answer has one.
func (e *e2e) deleteUser(name string) {
	e.t.Helper()
	var ans api.AccountAnswer
	e.ok(e.send("DELETE", "/users/"+name, nil), 200, &ans)
	if ans.Job != nil {
		e.done(ans.Job.ID)
	}
}

// sids are the SIDs of the Samba users by name (pdbedit -L -v).
func (e *e2e) sids() map[string]string {
	e.t.Helper()
	users, err := samba.Tools{R: exe, T: host.FindTools("/")}.Users(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]string{}
	for _, u := range users {
		out[u.Name] = u.SID
	}
	return out
}

// accountFiles are /etc/passwd, /etc/shadow and /etc/group.
func accountFiles() map[string]string {
	out := map[string]string{}
	for _, f := range []string{"/etc/passwd", "/etc/shadow", "/etc/group"} {
		out[f] = fileText(f)
	}
	return out
}

// entryOf is the line of the account file for name, "" when there is none.
func entryOf(file, name string) string {
	for _, l := range strings.Split(fileText(file), "\n") {
		if strings.HasPrefix(l, name+":") {
			return l
		}
	}
	return ""
}

// field is the n-th field (from 0) of an entry of an account file.
func field(entry string, n int) string {
	f := strings.Split(entry, ":")
	if n >= len(f) {
		return ""
	}
	return f[n]
}

// Clients.

// smbclient runs smbclient as the user with the arguments and returns its output and exit status. The password goes to it
// in the environment (PASSWD), never as an argument: no process may show a password.
func (e *e2e) smbclient(user, pw string, args ...string) (string, int) {
	e.t.Helper()
	r := run.Exec{Env: append(slices.Clone(run.Env), "PASSWD="+pw)}
	cmd := run.Cmd{Path: e.tool("smbclient"), Args: append([]string{"-U", user}, args...), Timeout: 2 * time.Minute}
	out, err := r.Run(ctx, cmd)
	status := 0
	if err != nil {
		var re *run.Error
		if !errors.As(err, &re) || re.Status < 0 {
			e.t.Fatalf("smbclient did not run: %v", err)
		}
		status = re.Status
	}
	e.statuses = append(e.statuses, fmt.Sprintf("%3d  %s  (the password in PASSWD)", status, cmd))
	return out, status
}

// smbAt is smbclient on the share at addr as the user with the commands; it says whether that worked (exit status 0 and
// no NT_STATUS_ in the output), with the output.
func (e *e2e) smbAt(addr, share, user, pw, cmds string, args ...string) (bool, string) {
	e.t.Helper()
	out, status := e.smbclient(user, pw, append([]string{"//" + addr + "/" + share, "-c", cmds}, args...)...)
	ok := status == 0 && !strings.Contains(out, "NT_STATUS_")
	return ok, fmt.Sprintf("--- //%s/%s: %s as %s: %s\n%s", addr, share, cmds, user, yesNo(ok), withStatus(out, status))
}

// can has the user do the commands on the share at 127.0.0.1, which has to work; it returns the log.
func (e *e2e) can(share, user, pw, cmds string) string {
	e.t.Helper()
	ok, out := e.smbAt("127.0.0.1", share, user, pw, cmds)
	if !ok {
		e.t.Errorf("%s as %s on %s: refused, want it to work\n%s", cmds, user, share, out)
	}
	return out
}

// cannot has the user try the commands on the share at 127.0.0.1, which have to be refused with the status text want.
func (e *e2e) cannot(share, user, pw, cmds, want string) string {
	e.t.Helper()
	ok, out := e.smbAt("127.0.0.1", share, user, pw, cmds)
	if ok || !strings.Contains(out, want) {
		e.t.Errorf("%s as %s on %s: worked %v, want a refusal with %s\n%s", cmds, user, share, ok, want, out)
	}
	return out
}

// listed are the shares that smbclient -L lists for the user. The listing itself has to work (exit status 0 and the line
// of IPC$, which every user sees), so that a share that is not listed is not one that a broken listing left out.
func (e *e2e) listed(user, pw string) ([]string, string) {
	e.t.Helper()
	out, status := e.smbclient(user, pw, "-L", "//127.0.0.1", "-g")
	var names []string
	ipc := false
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Split(l, "|"); len(f) >= 2 && f[0] == "Disk" {
			names = append(names, f[1])
		}
		ipc = ipc || strings.HasPrefix(l, "IPC|IPC$|")
	}
	if status != 0 || !ipc {
		e.t.Errorf("smbclient -L as %s did not list (exit status %d, IPC$ listed %v):\n%s", user, status, ipc, out)
	}
	return names, fmt.Sprintf("--- smbclient -L as %s\n%s", user, withStatus(out, status))
}

// holdOpen connects as the user to the share and opens the file, and keeps it open until the client is closed; it
// waits until smbstatus shows it.
func (e *e2e) holdOpen(share, user, pw, file string) *client {
	e.t.Helper()
	cmd := exec.Command(e.tool("smbclient"), "//127.0.0.1/"+share, "-U", user)
	cmd.Env, cmd.Dir = append(slices.Clone(run.Env), "PASSWD="+pw), "/"
	in, err := cmd.StdinPipe()
	if err != nil {
		e.t.Fatal(err)
	}
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	cl := &client{cmd: cmd, in: in, out: out}
	cl.send("open " + file)
	e.waitStatus(file, "-L")
	return cl
}

// Files.

// dir makes the folder p with the owner and the mode, whatever the umask, and old times.
func (e *e2e) dir(p string, uid, gid int, mode os.FileMode) {
	e.t.Helper()
	if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		e.t.Fatal(err)
	}
	e.own(p, uid, gid, mode)
}

// file writes the file p with the text, the owner, the mode and old times.
func (e *e2e) file(p, text string, uid, gid int, mode os.FileMode) {
	e.t.Helper()
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.own(p, uid, gid, mode)
}

// own gives p the owner, the mode (the bits as chmod takes them: 0o2775 is setgid) and old times.
func (e *e2e) own(p string, uid, gid int, mode os.FileMode) {
	e.t.Helper()
	if err := os.Lchown(p, uid, gid); err != nil {
		e.t.Fatal(err)
	}
	if err := unix.Chmod(p, uint32(mode)); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(p, old, old); err != nil {
		e.t.Fatal(err)
	}
}

// statOf is the owner, the group and the permission bits of p (a link is not followed).
func statOf(p string) (uid, gid int, mode uint32, err error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return 0, 0, 0, err
	}
	return int(st.Uid), int(st.Gid), st.Mode & 0o7777, nil
}

// wantOwner checks the owner, the group and the permission bits of p.
func (e *e2e) wantOwner(p string, uid, gid int, mode uint32) {
	e.t.Helper()
	u, g, m, err := statOf(p)
	if err != nil || u != uid || g != gid || m != mode {
		e.t.Errorf("%s: %d:%d %04o (%v), want %d:%d %04o", p, u, g, m, err, uid, gid, mode)
	}
}

// sum is the sha256 of the file's content.
func sum(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
