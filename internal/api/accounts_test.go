package api

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss/sdk/sdktest"
)

// accountAnswerOf is the answer of an account route.
func accountAnswerOf(t *testing.T, a answer) AccountAnswer {
	t.Helper()
	return decodeAs[AccountAnswer](t, a.want(t, 200))
}

// stdinOf is what the API gave the command on stdin.
func (f *harness) stdinOf(cmd string) (string, bool) {
	for _, c := range f.host.Calls {
		if c.Cmd.String() == cmd {
			return c.Cmd.Stdin, true
		}
	}
	return "", false
}

// index is the position of what ran in order, -1 when it did not.
func index(order []string, what string) int { return slices.Index(order, what) }

// A pure Samba user: an account without home and login in the marker group, then its Samba entry with the password on
// stdin. In diskless mode lbu commit saves it right after.
func TestAddSambaUser(t *testing.T) {
	f := newHarness(t)
	got := accountAnswerOf(t, f.send("POST", "/users/samba", map[string]string{"name": "carla", "password": "geheim"}, admin))
	if got.Job != nil {
		t.Errorf("a job for a name that the config does not have: %+v", got.Job)
	}
	if l := f.line(accounts.PasswdFile, "carla"); !strings.HasSuffix(l, ":103::/home/carla:/sbin/nologin") {
		t.Errorf("passwd %q", l)
	}
	if l := f.line(accounts.ShadowFile, "carla"); !strings.HasPrefix(l, "carla:!:") {
		t.Errorf("shadow %q", l)
	}
	if in, ok := f.stdinOf(pdbedit + " -a -u carla -t"); !ok || in != "geheim\ngeheim\n" {
		t.Errorf("pdbedit -a: %v, stdin %q", ok, in)
	}
	order := f.ran()
	if i, j := index(order, "busybox: adduser -D -H -s /sbin/nologin -G edelweiss-samba carla"), index(order, "host: "+pdbedit+" -a -u carla -t"); i < 0 || j < i {
		t.Errorf("the account, then its Samba entry: %v", order)
	}
	if slices.Contains(f.host.Commands(), lbu+" commit") {
		t.Errorf("lbu commit in sys mode")
	}

	f.diskless()
	accountAnswerOf(t, f.send("POST", "/users/samba", map[string]string{"name": "dana", "password": "geheim"}, admin))
	order = f.ran()
	if i, j := index(order, "host: "+pdbedit+" -a -u dana -t"), index(order, "host: "+lbu+" commit"); i < 0 || j < i {
		t.Errorf("lbu commit after the change: %v", order)
	}

	// refusals: a name that has a Samba entry, a name Samba does not take, no password, a reserved name
	f.send("POST", "/users/samba", map[string]string{"name": "anna", "password": "x"}, admin).wantError(t, 409, "exists")
	if e := f.send("POST", "/users/samba", map[string]string{"name": "Carla", "password": "x"}, admin).wantError(t, 400, "invalid_input"); e["field"] != "name" {
		t.Errorf("upper case: %v", e)
	}
	if e := f.send("POST", "/users/samba", map[string]string{"name": "erik"}, admin).wantError(t, 400, "invalid_input"); e["field"] != "password" {
		t.Errorf("no password: %v", e)
	}
	if e := f.send("POST", "/users/samba", map[string]string{"name": "edelweiss-share", "password": "x"}, admin).wantError(t, 400, "invalid_input"); e["field"] != "name" {
		t.Errorf("reserved: %v", e)
	}
	f.send("POST", "/users/samba", map[string]string{"name": "ben", "password": "x"}, admin).wantError(t, 409, "exists")
	f.send("POST", "/users/samba", "{", admin).wantError(t, 400, "invalid_json")

	// the Samba entry fails: the account goes again
	f.host.Results = map[string]run.FakeResult{pdbedit + " -a -u erik -t": {Status: 1, Output: "pdbedit failed"}}
	f.send("POST", "/users/samba", map[string]string{"name": "erik", "password": "geheim"}, admin).wantError(t, 500, "failed")
	if l := f.line(accounts.PasswdFile, "erik"); l != "" {
		t.Errorf("the account stays: %q", l)
	}
}

// A host user gets Samba access with their Linux password, which /etc/shadow has to confirm: a wrong one is refused before
// anything is changed.
func TestAddHostUserWrongPassword(t *testing.T) {
	f := newHarness(t)
	f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"carl:x:3002:3001::/home/carl:/bin/ash\n")
	f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"carl:"+benShadowHash(t)+":20761:0:99999:7:::\n", 0o640)

	f.send("POST", "/users/host", map[string]string{"name": "carl", "password": "falsch"}, admin).wantError(t, 409, "wrong_password")
	for _, c := range f.host.Commands() {
		if strings.HasPrefix(c, pdbedit+" -a") {
			t.Errorf("a wrong password reached Samba: %s", c)
		}
	}
	if got := accountAnswerOf(t, f.send("POST", "/users/host", map[string]string{"name": "carl", "password": benPassword}, admin)); got.Job != nil {
		t.Errorf("job %+v", got.Job)
	}
	if in, ok := f.stdinOf(pdbedit + " -a -u carl -t"); !ok || in != benPassword+"\n"+benPassword+"\n" {
		t.Errorf("pdbedit -a: %v, stdin %q", ok, in)
	}
	if len(f.bb.Calls) != 0 {
		t.Errorf("busybox for a host user: %v", f.bb.Commands())
	}
	// a system account, an account of the extension, one that is not there, one that has Samba already
	f.send("POST", "/users/host", map[string]string{"name": "sshd", "password": "x"}, admin).wantError(t, 409, "system_account")
	f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"dora:x:3003:103::/home/dora:/sbin/nologin\n")
	f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"dora:!:20761:0:99999:7:::\n", 0o640)
	f.send("POST", "/users/host", map[string]string{"name": "dora", "password": "x"}, admin).wantError(t, 409, "system_account")
	f.send("POST", "/users/host", map[string]string{"name": "zoe", "password": "x"}, admin).wantError(t, 404, "not_found")
	f.send("POST", "/users/host", map[string]string{"name": "ben", "password": benPassword}, admin).wantError(t, 409, "exists")
}

// A name that the config still has without a Samba entry (the user was deleted elsewhere) is dropped from the config
// before it is added again, so that the new user gets no right of the old one: the add waits for the job that drops it,
// and adds only after that job is done. When the job fails, nothing is added.
func TestStaleNamesDroppedBeforeAdd(t *testing.T) {
	stale := func(t *testing.T) *harness {
		f := newHarness(t)
		f.samba(map[string]string{"anna": sidBase + "1000"}) // ben and eve are gone, the config still has them
		fotos := f.fotos()
		fotos.Rights["eve"] = samba.Write
		fotos.PerUser = true
		fotos.Folders = map[string]map[string]samba.Right{"sub": {"anna": samba.Read, "ben": samba.Read, "eve": samba.Read}}
		f.config(fotos)
		return f
	}
	reload := "job: /usr/bin/smbcontrol smbd reload-config"

	t.Run("host user, the drop done", func(t *testing.T) {
		f := stale(t)
		got := accountAnswerOf(t, f.send("POST", "/users/host", map[string]string{"name": "ben", "password": benPassword}, admin))
		if got.Job == nil || got.Job.Kind != "user-drop" || got.Job.Target != "ben" || got.Job.Status != jobs.Done {
			t.Fatalf("job %+v, want the user-drop of ben, done", got.Job)
		}
		order := f.ran()
		if i, j := index(order, reload), index(order, "host: "+pdbedit+" -a -u ben -t"); i < 0 || j < i {
			t.Errorf("the config is written before the add: %v", order)
		}
		sh := f.loaded().Shares[0]
		if want := map[string]samba.Right{"anna": samba.Write, "eve": samba.Write}; !maps.Equal(sh.Rights, want) {
			t.Errorf("rights %v, want %v", sh.Rights, want)
		}
		if want := map[string]samba.Right{"anna": samba.Read, "eve": samba.Read}; !maps.Equal(sh.Folders["sub"], want) {
			t.Errorf("folder rights %v, want %v", sh.Folders["sub"], want)
		}
		if _, ok := f.sambaUsers()["ben"]; !ok {
			t.Errorf("ben has no Samba entry")
		}
	})
	t.Run("Samba user, the drop done", func(t *testing.T) {
		f := stale(t)
		got := accountAnswerOf(t, f.send("POST", "/users/samba", map[string]string{"name": "eve", "password": "neu"}, admin))
		if got.Job == nil || got.Job.Target != "eve" || got.Job.Status != jobs.Done {
			t.Fatalf("job %+v", got.Job)
		}
		order := f.ran()
		if i, j := index(order, reload), index(order, "busybox: adduser -D -H -s /sbin/nologin -G edelweiss-samba eve"); i < 0 || j < i {
			t.Errorf("the config is written before the account is made: %v", order)
		}
		if _, ok := f.loaded().Shares[0].Rights["eve"]; ok {
			t.Errorf("eve keeps the right of the old eve")
		}
	})
	t.Run("the drop failed", func(t *testing.T) {
		f := stale(t)
		f.jobFail = func(c run.Cmd) error {
			if c.Path == testparm {
				return &run.Error{Path: c.Path, Args: c.Args, Status: 1, Output: "bad"}
			}
			return nil
		}
		for _, c := range []struct{ path, name, password string }{{"/users/host", "ben", benPassword}, {"/users/samba", "eve", "neu"}} {
			e := f.send("POST", c.path, map[string]string{"name": c.name, "password": c.password}, admin).wantError(t, 500, "failed")
			id, _ := e["job"].(string)
			st, err := f.api.Jobs.Get(id)
			if err != nil || st.Status != jobs.Failed || st.Target != c.name {
				t.Errorf("%s: details %v: job %+v, %v; want the failed user-drop of %s", c.name, e, st, err, c.name)
			}
			if _, ok := f.sambaUsers()[c.name]; ok {
				t.Errorf("%s was added", c.name)
			}
			if _, ok := f.loaded().Shares[0].Rights[c.name]; !ok {
				t.Errorf("the config lost %s although the drop failed", c.name)
			}
		}
		if f.line(accounts.PasswdFile, "eve") != "" || len(f.bb.Calls) != 0 {
			t.Errorf("an account was made: %v", f.bb.Commands())
		}
	})
	t.Run("nothing stale", func(t *testing.T) {
		f := stale(t)
		f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"carl:x:3002:3001::/home/carl:/bin/ash\n")
		f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"carl:"+benShadowHash(t)+":20761:0:99999:7:::\n", 0o640)
		if got := accountAnswerOf(t, f.send("POST", "/users/host", map[string]string{"name": "carl", "password": benPassword}, admin)); got.Job != nil {
			t.Errorf("a job without a stale name: %+v", got.Job)
		}
		if len(f.launcher.launched()) != 0 {
			t.Errorf("launched %v", f.launcher.launched())
		}
	})
	// a runner that is gone without ending its job ends the wait at once: the job never got done, and nothing is added
	t.Run("the runner died", func(t *testing.T) {
		f := stale(t)
		f.launcher.dead = true
		answered := make(chan answer, 1)
		go func() {
			answered <- f.send("POST", "/users/samba", map[string]string{"name": "eve", "password": "neu"}, admin)
		}()
		var a answer
		select {
		case a = <-answered:
		case <-time.After(20 * time.Second):
			t.Fatal("the add still waits for a runner that is gone")
		}
		e := a.wantError(t, 500, "failed")
		id, _ := e["job"].(string)
		if st, err := f.api.Jobs.Get(id); err != nil || st.Kind != "user-drop" || st.Status == jobs.Done {
			t.Errorf("details %v: job %+v, %v", e, st, err)
		}
		if f.line(accounts.PasswdFile, "eve") != "" || len(f.bb.Calls) != 0 {
			t.Errorf("an account was made: %v", f.bb.Commands())
		}
	})
	// a runner that cannot be started: its job is ended failed, and the answer names it
	t.Run("the runner could not be started", func(t *testing.T) {
		f := stale(t)
		f.launcher.err = errors.New("no exec")
		e := f.send("POST", "/users/host", map[string]string{"name": "ben", "password": benPassword}, admin).wantError(t, 500, "failed")
		id, _ := e["job"].(string)
		if st, err := f.api.Jobs.Get(id); err != nil || st.Status != jobs.Failed || !strings.Contains(st.Error, "no exec") {
			t.Errorf("details %v: job %+v, %v", e, st, err)
		}
		if _, ok := f.sambaUsers()["ben"]; ok {
			t.Errorf("ben was added")
		}
	})
}

// Removing the Samba access of a host user keeps the account: only the Samba entry goes, and then the name goes from the
// config in a job. lbu commit saves the change before the job starts, which commits again at its end.
func TestDeleteHostUserKeepsAccount(t *testing.T) {
	f := newHarness(t)
	f.diskless()
	passwd := f.read(accounts.PasswdFile)
	got := accountAnswerOf(t, f.send("DELETE", "/users/ben", nil, admin))
	if got.Job == nil || got.Job.Kind != "user-drop" || got.Job.Target != "ben" {
		t.Fatalf("job %+v", got.Job)
	}
	f.launcher.wg.Wait()
	if _, ok := f.sambaUsers()["ben"]; ok {
		t.Errorf("ben keeps the Samba entry")
	}
	if f.read(accounts.PasswdFile) != passwd {
		t.Errorf("the accounts changed:\n%s", f.read(accounts.PasswdFile))
	}
	if want := map[string]samba.Right{"anna": samba.Write}; !maps.Equal(f.loaded().Shares[0].Rights, want) {
		t.Errorf("rights %v, want %v", f.loaded().Shares[0].Rights, want)
	}
	order := f.ran()
	remove, commit, job, jobCommit := index(order, "host: "+pdbedit+" -x ben"), index(order, "host: "+lbu+" commit"),
		index(order, "job: "+testparm+" -s /run/edelweiss-ext-samba/jobs/"+got.Job.ID+"/smb.conf"), index(order, "job: "+lbu+" commit")
	if remove < 0 || commit < remove || job < commit || jobCommit < job {
		t.Errorf("pdbedit -x, lbu commit, then the job and its lbu commit: %v", order)
	}
	if len(f.bb.Calls) != 0 {
		t.Errorf("busybox: %v", f.bb.Commands())
	}

	// a user that no share names: no job
	f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"carl:x:3002:3001::/home/carl:/bin/ash\n")
	f.samba(map[string]string{"anna": sidBase + "1000", "carl": sidBase + "1005"})
	if got := accountAnswerOf(t, f.send("DELETE", "/users/carl", nil, admin)); got.Job != nil {
		t.Errorf("job %+v", got.Job)
	}
	f.send("DELETE", "/users/zoe", nil, admin).wantError(t, 404, "not_found")
	f.send("DELETE", "/users/sshd", nil, admin).wantError(t, 404, "not_found")
}

// A delete that worked is answered 200 also when the job that drops the name from the config cannot be started: the
// entry and the account are gone and saved, and jobError says why the name stays in the config (the page offers to remove
// orphaned names).
func TestDeleteUserJobNotStarted(t *testing.T) {
	f := newHarness(t)
	f.launcher.err = errors.New("no runner")
	got := accountAnswerOf(t, f.send("DELETE", "/users/anna", nil, admin))
	if got.Job != nil || !strings.Contains(got.JobError, "no runner") {
		t.Errorf("answer %+v", got)
	}
	if _, ok := f.sambaUsers()["anna"]; ok || f.line(accounts.PasswdFile, "anna") != "" {
		t.Error("anna is still there")
	}
}

// A pure Samba user goes with its account: the Samba entry, the lines of passwd and shadow, its membership of the marker
// group, and its name in the config.
func TestDeleteSambaUserRemovesAccount(t *testing.T) {
	f := newHarness(t)
	got := accountAnswerOf(t, f.send("DELETE", "/users/anna", nil, admin))
	if got.Job == nil || got.Job.Target != "anna" {
		t.Fatalf("job %+v", got.Job)
	}
	f.launcher.wg.Wait()
	if _, ok := f.sambaUsers()["anna"]; ok {
		t.Errorf("anna keeps the Samba entry")
	}
	if l := f.line(accounts.PasswdFile, "anna") + f.line(accounts.ShadowFile, "anna"); l != "" {
		t.Errorf("the account stays: %q", l)
	}
	if l := f.line(accounts.GroupFile, "edelweiss-samba"); l != "edelweiss-samba:x:103:" {
		t.Errorf("marker group %q", l)
	}
	if want := map[string]samba.Right{"ben": samba.Read}; !maps.Equal(f.loaded().Shares[0].Rights, want) {
		t.Errorf("rights %v, want %v", f.loaded().Shares[0].Rights, want)
	}
	if st, err := f.api.Jobs.Get(got.Job.ID); err != nil || st.Status != jobs.Done {
		t.Errorf("the job %+v, %v", st, err)
	}
}

// The Samba password of a pure Samba user is set with smbpasswd -s, the password on stdin.
func TestSetPassword(t *testing.T) {
	f := newHarness(t)
	f.diskless()
	if got := accountAnswerOf(t, f.send("POST", "/users/anna/password", map[string]string{"password": "neu neu"}, admin)); got.Job != nil {
		t.Errorf("job %+v", got.Job)
	}
	if in, ok := f.stdinOf(smbpasswd + " -s anna"); !ok || in != "neu neu\nneu neu\n" {
		t.Errorf("smbpasswd: %v, stdin %q", ok, in)
	}
	order := f.ran()
	if i, j := index(order, "host: "+smbpasswd+" -s anna"), index(order, "host: "+lbu+" commit"); i < 0 || j < i {
		t.Errorf("lbu commit after the change: %v", order)
	}
	// data mode: /etc is RAM there too
	f.write("/proc/mounts", "tmpfs / tmpfs rw 0 0\n/dev/sda2 /var ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n")
	before := len(f.host.Calls)
	accountAnswerOf(t, f.send("POST", "/users/anna/password", map[string]string{"password": "neu"}, admin))
	if cmds := f.host.Commands()[before:]; !slices.Contains(cmds, lbu+" commit") {
		t.Errorf("data mode: ran %v, want lbu commit", cmds)
	}
	f.send("POST", "/users/zoe/password", map[string]string{"password": "x"}, admin).wantError(t, 404, "not_found")
	if e := f.send("POST", "/users/anna/password", map[string]string{"password": "a\nb"}, admin).wantError(t, 400, "invalid_input"); e["field"] != "password" {
		t.Errorf("a line break: %v", e)
	}
}

// A host user has one password, the Linux one: the Samba password is only set when /etc/shadow confirms it, so a wrong one
// is refused before smbpasswd runs, and the right one gives Samba that password.
func TestSetPasswordHostUser(t *testing.T) {
	f := newHarness(t)
	before := len(f.host.Calls)
	f.send("POST", "/users/ben/password", map[string]string{"password": "falsch"}, admin).wantError(t, 409, "wrong_password")
	for _, c := range f.host.Commands()[before:] {
		if strings.Contains(c, "smbpasswd") {
			t.Errorf("a wrong password reached Samba: %s", c)
		}
	}
	if got := accountAnswerOf(t, f.send("POST", "/users/ben/password", map[string]string{"password": benPassword}, admin)); got.Job != nil {
		t.Errorf("job %+v", got.Job)
	}
	if in, ok := f.stdinOf(smbpasswd + " -s ben"); !ok || in != benPassword+"\n"+benPassword+"\n" {
		t.Errorf("smbpasswd: %v, stdin %q", ok, in)
	}
}

// The Linux password of a host user is checked again under the account lock, right before pdbedit -a and smbpasswd -s:
// edelweiss-ext-users may lock the user, or change the password, after the first check and before the lock is free.
func TestHostUserCheckedUnderAccountLock(t *testing.T) {
	for _, c := range []struct{ name, target, user, cmd string }{
		{"add", "/users/host", "carl", pdbedit + " -a"},
		{"password", "/users/ben/password", "ben", smbpasswd + " -s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHarness(t)
			f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"carl:x:3002:3001::/home/carl:/bin/ash\n")
			f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"carl:"+benShadowHash(t)+":20761:0:99999:7:::\n", 0o640)
			f.api.Accounts.Lock.Wait = 10 * time.Second
			release, err := f.api.Accounts.Lock.Take(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]string{"name": c.user, "password": benPassword}
			done := make(chan answer)
			go func() { done <- f.send("POST", c.target, body, admin) }()
			// the route has read the Samba users and checked the password, and waits for the lock
			if _, ok := pollFor(5*time.Second, func() bool { return slices.Contains(f.host.Commands(), pdbedit+" -L -v") }); !ok {
				t.Fatal("the route did not read the Samba users")
			}
			time.Sleep(200 * time.Millisecond)
			f.file(accounts.ShadowFile, strings.Replace(f.read(accounts.ShadowFile), c.user+":$", c.user+":!$", 1), 0o640)
			release()
			(<-done).wantError(t, 409, "wrong_password")
			for _, cmd := range f.host.Commands() {
				if strings.HasPrefix(cmd, c.cmd) {
					t.Errorf("Samba got the password of a user that was locked meanwhile: %s", cmd)
				}
			}
		})
	}
}

// pollFor checks cond every 10 ms until it is true or limit has passed.
func pollFor(limit time.Duration, cond func() bool) (time.Duration, bool) {
	start := time.Now()
	for !cond() {
		if time.Since(start) > limit {
			return time.Since(start), false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(start), true
}

// A password goes to the Samba tools on stdin only: it is in no argument, no log, no answer and no job, also when the
// commands fail.
func TestPasswordNeverLogged(t *testing.T) {
	f := newHarness(t)
	const pw = "-Geh eim:ä ß"
	fotos := f.fotos()
	fotos.Rights["carla"] = samba.Write // a stale name: the add of carla makes a job first
	f.config(fotos)
	f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"carl:x:3002:3001::/home/carl:/bin/ash\n")
	f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"carl:"+benShadowHash(t)+":20761:0:99999:7:::\n", 0o640)
	f.host.Results = map[string]run.FakeResult{
		pdbedit + " -a -u carla -t": {Status: 1, Output: "pdbedit failed"},
		smbpasswd + " -s anna":      {Status: 1, Output: "smbpasswd failed"},
	}
	var answers []string
	for _, r := range []struct{ method, path, name string }{
		{"POST", "/users/samba", "carla"}, {"POST", "/users/host", "carl"}, {"POST", "/users/anna/password", ""},
	} {
		body := map[string]string{"password": pw}
		if r.name != "" {
			body["name"] = r.name
		}
		answers = append(answers, f.send(r.method, r.path, body, admin).raw)
	}
	f.launcher.wg.Wait()
	if !strings.Contains(f.logged(), "server error") {
		t.Errorf("no 500 was logged: %q", f.logged())
	}
	for what, s := range map[string]string{"the log": f.logged(), "the answers": strings.Join(answers, "\n"),
		"the jobs":      strings.Join(f.specs(), "\n"),
		"the arguments": strings.Join(slices.Concat(f.host.Commands(), f.cmds.Commands(), f.bb.Commands()), "\n")} {
		if strings.Contains(s, pw) || strings.Contains(s, "Geh eim") {
			t.Errorf("the password is in %s: %s", what, s)
		}
	}
	if in, ok := f.stdinOf(pdbedit + " -a -u carla -t"); !ok || in != pw+"\n"+pw+"\n" {
		t.Errorf("pdbedit -a: %v, stdin %q", ok, in)
	}
	if len(f.specs()) == 0 || len(f.bb.Calls) == 0 {
		t.Errorf("no job (%d files) or no busybox applet (%v) was looked at", len(f.specs()), f.bb.Commands())
	}

	// the body of the request, which the server uses again, holds no byte of it after the decode
	app := fiber.New()
	var left []byte
	app.Post("/x", func(c fiber.Ctx) error {
		var req userRequest
		err := decode(c, &req)
		left = slices.Clone(c.BodyRaw())
		if err != nil || req.Password != pw {
			t.Errorf("decode: %v, password %q", err, req.Password)
		}
		return c.SendStatus(204)
	})
	resp := sdktest.Do(t, app, sdktest.Request("POST", "/x", strings.NewReader(`{"name":"carla","password":"`+pw+`"}`), admin))
	resp.Body.Close()
	if len(left) == 0 || slices.ContainsFunc(left, func(b byte) bool { return b != 0 }) {
		t.Errorf("the body after the decode: %q", left)
	}
}

// Every account route is for the admin mode: without it, nothing is read or run.
func TestAccountRoutesNeedAdmin(t *testing.T) {
	f := newHarness(t)
	for _, r := range []struct{ method, path string }{
		{"POST", "/users/samba"}, {"POST", "/users/host"}, {"POST", "/users/ben/password"}, {"DELETE", "/users/ben"},
		{"POST", "/service-accounts"}, {"POST", "/browse/mkdir"},
	} {
		f.send(r.method, r.path, map[string]string{"name": "carla", "password": "geheim", "path": "/mnt/disk/data"}, plain).
			wantError(t, 403, "admin_required")
	}
	if len(f.host.Calls) != 0 || len(f.bb.Calls) != 0 || len(f.launcher.launched()) != 0 {
		t.Errorf("ran %v, busybox %v, jobs %v", f.host.Commands(), f.bb.Commands(), f.launcher.launched())
	}
}

// The service account of a folder whose owner has no account has exactly that UID and the group of the GID; the shares of
// the folder are rendered again in a job.
func TestServiceAccount(t *testing.T) {
	f := newHarness(t)
	f.diskless()
	// the owner of the folders (the user of the test) has no account any more
	var keep []string
	for _, l := range strings.SplitAfter(f.read(accounts.PasswdFile), "\n") {
		if !strings.HasPrefix(l, "myUser:") {
			keep = append(keep, l)
		}
	}
	f.write(accounts.PasswdFile, strings.Join(keep, ""))
	f.send("POST", "/service-accounts", map[string]string{"path": "/mnt/disk/data/"}, admin).wantError(t, 400, "invalid_input")
	f.send("POST", "/service-accounts", map[string]string{"path": "/proc"}, admin).wantError(t, 400, "invalid_input")

	got := accountAnswerOf(t, f.send("POST", "/service-accounts", map[string]string{"path": "/mnt/disk/data"}, admin))
	wantUser := "smb-" + uidString()
	if got.User != wantUser || got.Group != "myGroup" || got.Job == nil || got.Job.Kind != "service-account" {
		t.Errorf("answer %+v", got)
	}
	if l := f.line(accounts.PasswdFile, wantUser); !strings.Contains(l, ":"+uidString()+":") {
		t.Errorf("passwd %q", l)
	}
	order := f.ran()
	if i := index(order, "host: "+lbu+" commit"); i < 0 || i < index(order, "busybox: addgroup "+wantUser+" edelweiss-samba") {
		t.Errorf("lbu commit after the account: %v", order)
	}
	f.launcher.wg.Wait()
	if st, err := f.api.Jobs.Get(got.Job.ID); err != nil || st.Status != jobs.Done {
		t.Errorf("the job %+v, %v", st, err)
	}
	// the owner has an account now
	if e := f.send("POST", "/service-accounts", map[string]string{"path": "/mnt/disk/data"}, admin).wantError(t, 409, "exists"); e["field"] != "path" {
		t.Errorf("an owner with an account: %v", e)
	}
}

// The calls of pdbedit and smbpasswd run under the account lock that edelweiss-ext-users takes too: while it is held, the
// change is busy and Samba's user database is not touched.
func TestAccountLockHeld(t *testing.T) {
	f := newHarness(t)
	release, err := f.api.Accounts.Lock.Take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"carl:x:3002:3001::/home/carl:/bin/ash\n")
	f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"carl:"+benShadowHash(t)+":20761:0:99999:7:::\n", 0o640)
	f.send("POST", "/users/host", map[string]string{"name": "carl", "password": benPassword}, admin).wantError(t, 409, "busy")
	f.send("POST", "/users/ben/password", map[string]string{"password": benPassword}, admin).wantError(t, 409, "busy")
	f.send("DELETE", "/users/ben", nil, admin).wantError(t, 409, "busy")
	for _, c := range f.host.Commands() {
		if c != pdbedit+" -L -v" {
			t.Errorf("ran %s while the account lock is held", c)
		}
	}
	if _, ok := f.sambaUsers()["ben"]; !ok {
		t.Errorf("ben lost the Samba entry")
	}
	release()
	accountAnswerOf(t, f.send("POST", "/users/host", map[string]string{"name": "carl", "password": benPassword}, admin))
}

// The GID of a folder's owner that is a group of the system (not users) is refused with the group in the details, which the
// page names.
func TestServiceAccountSystemGroup(t *testing.T) {
	f := newHarness(t)
	f.write(accounts.GroupFile, f.read(accounts.GroupFile)+"wheel:x:10:root\n")
	f.api.OwnerOf = func(root safefs.Root, p string, db *accounts.DB) (paths.Ownership, error) {
		return paths.Ownership{UID: 1026, GID: 10}, nil
	}
	e := f.send("POST", "/service-accounts", map[string]string{"path": "/mnt/disk/data"}, admin).wantError(t, 409, "system_account")
	if e["group"] != "wheel" {
		t.Errorf("the refusal %v does not name the group", e)
	}
	if len(f.launcher.launched()) != 0 {
		t.Errorf("jobs %v", f.launcher.launched())
	}
}

// A folder of root gets no service account: Samba would write as root.
func TestServiceAccountRootOwned(t *testing.T) {
	f := newHarness(t)
	f.api.OwnerOf = func(root safefs.Root, p string, db *accounts.DB) (paths.Ownership, error) {
		return paths.Ownership{UID: 0, GID: 0, User: "root", Group: "root"}, nil
	}
	f.send("POST", "/service-accounts", map[string]string{"path": "/mnt/disk/data"}, admin).wantError(t, 409, "root_owned")
	if len(f.bb.Calls) != 0 || len(f.launcher.launched()) != 0 {
		t.Errorf("busybox %v, jobs %v", f.bb.Commands(), f.launcher.launched())
	}
}
