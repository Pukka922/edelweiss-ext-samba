package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The kinds of the jobs that the account routes start.
const (
	kindUserDrop       = "user-drop"
	kindServiceAccount = "service-account"
)

// lbuTimeout is the time limit of lbu commit.
const lbuTimeout = 2 * time.Minute

// AccountAnswer is the answer of an account route: the job that changes the config after it (user-drop, service-account)
// when there is one, and the account that was made for a folder (POST /service-accounts). JobError says why the job of a
// delete that worked could not be started (the name stays in the config until it is removed).
type AccountAnswer struct {
	Job      *jobs.State `json:"job,omitempty"`
	JobError string      `json:"jobError,omitempty"`
	User     string      `json:"user,omitempty"`
	Group    string      `json:"group,omitempty"`
}

// userRequest is the body of POST /users/samba and POST /users/host.
type userRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// checkUser refuses a name that Samba does not take (accounts.ValidName: smbd looks a user up in lower case) and a
// password that cannot go to Samba on one line. The message never holds the password.
func checkUser(name, password string) error {
	if !accounts.ValidName(name) {
		return invalid("name", "the name has 1 to 32 characters of a-z, 0-9, _ and -, and starts with a letter or _")
	}
	if !accounts.ValidPassword(password) {
		return invalid("password", "the password has 1 to 256 bytes, without a line break or NUL")
	}
	return nil
}

// setUpConfig is the config of the extension: not_setup on a host that is not set up, failed when it cannot be read.
func (a *API) setUpConfig() (samba.Config, error) {
	cfg, set, err := samba.Load(a.fs())
	switch {
	case err != nil:
		return samba.Config{}, failed("the config cannot be read: %v", err)
	case !set:
		return samba.Config{}, refuse(actions.CodeNotSetup, "Samba is not set up by the extension on this host", nil)
	}
	return cfg, nil
}

// names reports whether the config names the user: in the rights of a share or of one of its folders.
func names(cfg samba.Config, user string) bool {
	for _, s := range cfg.Shares {
		if _, ok := s.Rights[user]; ok {
			return true
		}
		for _, rights := range s.Folders {
			if _, ok := rights[user]; ok {
				return true
			}
		}
	}
	return false
}

// withAccountLock runs fn under the account lock that edelweiss-ext-users takes too. fn calls no method of
// internal/accounts: each takes the lock itself, and a second flock of the same file waits for the first.
func (a *API) withAccountLock(ctx context.Context, fn func() error) error {
	release, err := a.service().Lock.Take(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// commit saves an account change in diskless and data mode, where /etc is RAM (and lbu keeps Samba's databases): right
// after the change, also when a job follows, so that the change stays when that job fails before its own commit.
func (a *API) commit(ctx context.Context) error {
	mode, err := a.fs().Mode()
	if err != nil {
		return failed("the change is made, but how Alpine runs cannot be read for lbu commit: %v", err)
	}
	if mode != host.Diskless && mode != host.Data {
		return nil
	}
	t := a.tools()
	if t.Lbu == "" {
		return failed("the change is made, but lbu is not installed to save it")
	}
	if _, err := a.Runner.Run(ctx, run.Cmd{Path: t.Lbu, Args: []string{"commit"}, Timeout: lbuTimeout}); err != nil {
		return failed("the change is made, but lbu commit failed: %v", err)
	}
	return nil
}

// dropStale drops the name from the config before it is added again: the config still names it without a Samba entry (a
// user deleted elsewhere), and a new user of that name must not get the old rights. The job (user-drop) runs under the
// job lock that the caller holds; the caller keeps its copy, waits for the end of the job, and goes on only when it is
// done, so that no other job runs before the add is finished. A job that did not end done is failed, with the job.
func (a *API) dropStale(ctx context.Context, lock *os.File, name string) (*jobs.State, error) {
	st, pid, err := a.launch(ctx, lock, actions.Request{Kind: kindUserDrop, User: name})
	if err != nil {
		return nil, toHTTP(err)
	}
	end, err := a.await(st.ID, pid)
	if err != nil {
		return nil, refuse(actions.CodeFailed, fmt.Sprintf("the job that drops %s from the config cannot be read: %v", name, err),
			map[string]any{"job": st.ID})
	}
	if end.Status != jobs.Done {
		return nil, refuse(actions.CodeFailed, fmt.Sprintf("%s could not be dropped from the config (%s %s), so it is not added", name, end.Status, end.Error),
			map[string]any{"job": st.ID})
	}
	return &end, nil
}

func (a *API) addSambaUser(c fiber.Ctx) error { return a.addUser(c, false) }
func (a *API) addHostUser(c fiber.Ctx) error  { return a.addUser(c, true) }

// addUser answers POST /users/samba and POST /users/host (admin mode), synchronously, under the job lock.
//
// A pure Samba user gets an account first (accounts.CreateSambaOnly: no home, no login, the marker group), which goes
// again when its Samba entry cannot be made. A host user (a normal UID, no account of the extension) gives the Linux
// password, which /etc/shadow has to confirm (wrong_password), first and again under the account lock right before
// pdbedit -a (edelweiss-ext-users may lock the user in between), and Samba gets the same. Either way the Samba entry is
// made with pdbedit -a, the password on stdin, under the account lock; then lbu commit. A name that has a Samba entry
// already exists. A name that the config still has without a Samba entry is dropped from it first (dropStale).
func (a *API) addUser(c fiber.Ctx, hostUser bool) error {
	var req userRequest
	if err := decode(c, &req); err != nil {
		return err
	}
	if err := checkUser(req.Name, req.Password); err != nil {
		return err
	}
	ctx := ctxOf(c)
	lock, err := a.takeJobLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := a.setUpConfig()
	if err != nil {
		return err
	}
	t := a.tools()
	users, err := a.sambaTools(t).Users(ctx)
	if err != nil {
		return toHTTP(err)
	}
	if hasEntry(users, req.Name) {
		return apiError(actions.CodeExists, fmt.Sprintf("%s has Samba access already", req.Name), "name", nil)
	}
	if hostUser {
		if err := a.checkHostUser(req.Name, req.Password); err != nil {
			return err
		}
	}
	var answer AccountAnswer
	if names(cfg, req.Name) {
		if answer.Job, err = a.dropStale(ctx, lock, req.Name); err != nil {
			return err
		}
	}
	if !hostUser {
		if err := a.service().CreateSambaOnly(ctx, req.Name); err != nil {
			return toHTTP(err)
		}
	}
	err = a.withAccountLock(ctx, func() error {
		if hostUser { // again: the user may have been locked, or got another password, while the lock was not held
			if err := a.checkHostUser(req.Name, req.Password); err != nil {
				return err
			}
		}
		return a.sambaTools(t).Add(ctx, req.Name, req.Password)
	})
	if err != nil {
		if !hostUser {
			if uerr := a.service().DeleteSambaOnly(context.WithoutCancel(ctx), req.Name); uerr != nil {
				return failed("%v; the account %s stays, removing it failed: %v", err, req.Name, uerr)
			}
		}
		return toHTTP(err)
	}
	if err := a.commit(ctx); err != nil {
		return err
	}
	return c.JSON(answer)
}

// checkHostUser refuses a name that is no host user (not_found, or system_account for a system account and an account
// of the extension) and a password that /etc/shadow does not confirm (wrong_password).
func (a *API) checkHostUser(name, password string) error {
	db, err := accounts.Load(a.Root, a.now())
	if err != nil {
		return failed("the accounts cannot be read: %v", err)
	}
	u, ok := db.User(name)
	switch {
	case !ok:
		return notFound(map[string]any{"user": name}, "there is no user %q", name)
	case accounts.SystemID(u.UID) || accounts.IsManaged(db, name):
		return refuse(accounts.CodeSystem, fmt.Sprintf("%q is no host user", name), nil)
	}
	err = accounts.Verifier{ShadowPath: filepath.Join(a.Root, accounts.ShadowFile), Now: a.now}.Verify(name, password)
	switch {
	case errors.Is(err, accounts.ErrWrongPassword):
		return refuse(codeWrongPassword, "that is not the Linux password of "+name, nil)
	case err != nil:
		return failed("the password cannot be checked: %v", err)
	}
	return nil
}

// setPassword answers POST /users/:name/password (admin mode): the Samba password of a user with a Samba entry, with
// smbpasswd -s, the password on stdin, under the account lock; then lbu commit. A host user has one password, the Linux
// one: it is verified against /etc/shadow first, as the add does (wrong_password), so that the Samba password of a host
// user is never another one. The Linux password stays.
func (a *API) setPassword(c fiber.Ctx) error {
	name, err := param(c)
	if err != nil {
		return err
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(c, &req); err != nil {
		return err
	}
	if err := checkUser(name, req.Password); err != nil {
		return err
	}
	ctx := ctxOf(c)
	lock, err := a.takeJobLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := a.setUpConfig(); err != nil {
		return err
	}
	t := a.tools()
	users, err := a.sambaTools(t).Users(ctx)
	if err != nil {
		return toHTTP(err)
	}
	if !hasEntry(users, name) {
		return notFound(map[string]any{"user": name}, "%s has no Samba access", name)
	}
	db, err := accounts.Load(a.Root, a.now())
	if err != nil {
		return failed("the accounts cannot be read: %v", err)
	}
	host := !accounts.IsSambaOnly(db, name)
	if host {
		if err := a.checkHostUser(name, req.Password); err != nil {
			return err
		}
	}
	if err := a.withAccountLock(ctx, func() error {
		if host { // again, under the lock (see addUser)
			if err := a.checkHostUser(name, req.Password); err != nil {
				return err
			}
		}
		return a.sambaTools(t).SetPassword(ctx, name, req.Password)
	}); err != nil {
		return toHTTP(err)
	}
	if err := a.commit(ctx); err != nil {
		return err
	}
	return c.JSON(AccountAnswer{})
}

// deleteUser answers DELETE /users/:name (admin mode). The Samba entry goes (pdbedit -x, under the account lock); a pure
// Samba user loses its account too (accounts.DeleteSambaOnly), a host user keeps it. lbu commit saves that, and then the
// name goes from every share and folder right in a job (user-drop), whose state the answer has: the page follows it. A
// job that cannot be started leaves the delete done: the answer is 200 with jobError.
func (a *API) deleteUser(c fiber.Ctx) error {
	name, err := param(c)
	if err != nil {
		return err
	}
	if !accounts.ValidName(name) {
		return notFound(map[string]any{"user": name}, "there is no Samba user %q", name)
	}
	ctx := ctxOf(c)
	lock, err := a.takeJobLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := a.setUpConfig()
	if err != nil {
		return err
	}
	t := a.tools()
	users, err := a.sambaTools(t).Users(ctx)
	if err != nil {
		return toHTTP(err)
	}
	db, err := accounts.Load(a.Root, a.now())
	if err != nil {
		return failed("the accounts cannot be read: %v", err)
	}
	entry, pure := hasEntry(users, name), accounts.IsSambaOnly(db, name)
	if !entry && !pure {
		return notFound(map[string]any{"user": name}, "there is no Samba user %q", name)
	}
	if entry {
		if err := a.withAccountLock(ctx, func() error { return a.sambaTools(t).Remove(ctx, name) }); err != nil {
			return toHTTP(err)
		}
	}
	if pure {
		if err := a.service().DeleteSambaOnly(ctx, name); err != nil {
			if entry { // the Samba entry is gone already
				if cerr := a.commit(ctx); cerr != nil {
					slog.Error("samba: the removal of a Samba entry is not saved", "user", name, "err", cerr)
				}
			}
			return toHTTP(err)
		}
	}
	if err := a.commit(ctx); err != nil {
		return err
	}
	var answer AccountAnswer
	if names(cfg, name) {
		// the delete is done and saved: a job that cannot start is no failure of it, and the answer says so
		if st, _, err := a.launch(ctx, lock, actions.Request{Kind: kindUserDrop, User: name}); err != nil {
			slog.Error("samba: the name of a deleted user cannot be dropped from the config", "user", name, "err", err)
			answer.JobError = err.Error()
		} else {
			answer.Job = &st
		}
	}
	return c.JSON(answer)
}

// serviceAccount answers POST /service-accounts {path} (admin mode): the account for the owner of the folder at path,
// which has none (accounts.CreateServiceAccount: smb-<uid> with exactly that UID, in the group of the GID). The folder is
// on a disk a share can be on and reached through no link; one of root is root_owned (no account is made for uid 0: root
// has its own, so this is a passwd without it), one whose owner has an account exists. lbu
// commit saves it. When shares have that folder, they are rendered again in a job (service-account): Render left them
// out. A folder that no share has yet (a share that is about to be added) gets the account alone.
func (a *API) serviceAccount(c fiber.Ctx) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := decode(c, &req); err != nil {
		return err
	}
	ctx := ctxOf(c)
	lock, err := a.takeJobLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := a.setUpConfig()
	if err != nil {
		return err
	}
	if _, _, err := a.onShareDisk("path", req.Path); err != nil {
		return err
	}
	root, err := safefs.Open(a.Root)
	if err != nil {
		return failed("%v", err)
	}
	defer root.Close()
	db, err := accounts.Load(a.Root, a.now())
	if err != nil {
		return failed("the accounts cannot be read: %v", err)
	}
	o, err := a.ownerOf(root, req.Path, db)
	switch {
	case err != nil:
		return dirError("path", req.Path, err)
	case o.UID == 0:
		return refuse(actions.CodeRootOwned, req.Path+" belongs to root", nil)
	case o.User != "" && o.Group != "":
		return apiError(actions.CodeExists, fmt.Sprintf("the owner %s:%s of %s has an account", o.User, o.Group, req.Path), "path", nil)
	}
	user, group, err := a.service().CreateServiceAccount(ctx, o.UID, o.GID)
	if err != nil {
		return toHTTP(err)
	}
	if err := a.commit(ctx); err != nil {
		return err
	}
	answer := AccountAnswer{User: user, Group: group}
	for _, s := range cfg.Shares {
		if s.Path == req.Path {
			st, _, err := a.launch(ctx, lock, actions.Request{Kind: kindServiceAccount, Path: req.Path})
			if err != nil {
				return toHTTP(err)
			}
			answer.Job = &st
			break
		}
	}
	return c.JSON(answer)
}
