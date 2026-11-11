package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// createJob answers POST /jobs (admin mode): it plans the change on a fresh look at the host and starts it as a job, 202
// with its state. Another change that runs is 409 busy.
func (a *API) createJob(c fiber.Ctx) error {
	var req actions.Request
	if err := decode(c, &req); err != nil {
		return err
	}
	lock, err := a.takeJobLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	st, _, err := a.launch(ctxOf(c), lock, req)
	if err != nil {
		return toHTTP(err)
	}
	return c.Status(fiber.StatusAccepted).JSON(st)
}

// job answers GET /jobs/:id, for everybody. An id that is not of the form of a job id is no job. A job that its runner
// left reads interrupted (Store.Get).
func (a *API) job(c fiber.Ctx) error {
	st, err := a.Jobs.Get(c.Params("id"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return notFound(nil, "there is no such job")
	case err != nil:
		return toHTTP(fmt.Errorf("reading the job: %w", err))
	}
	return c.JSON(st)
}

// cancel answers POST /jobs/:id/cancel (admin mode): it asks the job, which runs a step that can be cancelled, to stop,
// 204. The answer never means that the job stopped: it ends with the code cancelled, which the page watches. A job that
// cannot be cancelled now is 409 not_cancellable, and a job that is not there not_found. The body is not looked at.
func (a *API) cancel(c fiber.Ctx) error {
	err := a.Jobs.Cancel(c.Params("id"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return notFound(nil, "there is no such job")
	case errors.Is(err, jobs.ErrNotCancellable):
		return refuse(codeNotCancellable, "the job cannot be cancelled now", nil)
	case err != nil:
		return toHTTP(fmt.Errorf("cancelling the job: %w", err))
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// takeJobLock takes the job lock, which a job holds to its end: 409 busy while another change runs. The caller closes its
// copy when it is done; a job it launched holds its own.
func (a *API) takeJobLock() (*os.File, error) {
	lock, err := a.Jobs.TryLock()
	switch {
	case errors.Is(err, jobs.ErrBusy):
		return nil, refuse(codeBusy, "another change is running; try again", nil)
	case err != nil:
		return nil, failed("taking the job lock: %v", err)
	}
	return lock, nil
}

// launch plans the request on a fresh look at the host and starts it as a job, under the job lock that the caller holds
// (and keeps: the runner gets one of its own). The look is taken after the lock, so that no other change makes it stale
// before the job runs. The id of the job is made first, the plan stages its files in the job's directory, and the job is
// created with that id. A job whose runner could not be started is ended as failed while the lock is still held, so that
// it does not read as interrupted, and the error is a *startError that names it. The refusal of the plan is a
// *actions.Error. The pid is that of the runner (Launcher.Launch).
func (a *API) launch(ctx context.Context, lock *os.File, req actions.Request) (jobs.State, int, error) {
	env, err := a.env(ctx)
	if err != nil {
		return jobs.State{}, 0, err
	}
	now := a.now()
	id := a.Jobs.NewID(now)
	if env.JobDir, err = a.jobDir(id); err != nil {
		return jobs.State{}, 0, err
	}
	spec, err := actions.Build(req, env)
	if err != nil {
		return jobs.State{}, 0, err
	}
	spec.ID = id
	dir, st, err := a.Jobs.Create(lock, spec, now)
	if err != nil {
		return jobs.State{}, 0, fmt.Errorf("creating the job: %w", err)
	}
	pid, err := a.Launcher.Launch(dir, lock)
	if err != nil {
		msg := "the job could not be started: " + err.Error()
		if ferr := a.Jobs.Fail(dir, actions.CodeFailed, msg, a.now()); ferr != nil {
			slog.Error("samba: the job that could not be started cannot be ended", "job", st.ID, "err", ferr)
		}
		return jobs.State{}, 0, &startError{job: st.ID, msg: msg}
	}
	return st, pid, nil
}

// startError is a job whose runner could not be started: Create made it, and it is ended failed. The answer names it
// (details job).
type startError struct{ job, msg string }

func (e *startError) Error() string { return e.msg }

// jobDir is the directory of the job id as the host names it: the store's directory below the root, which is where the
// plan stages its files and the runner writes them (it puts the root before every path).
func (a *API) jobDir(id string) (string, error) {
	rel, err := filepath.Rel(a.Root, a.Jobs.Base)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("the jobs in %s are not below the root %s", a.Jobs.Base, a.Root)
	}
	return path.Join("/", filepath.ToSlash(rel), id), nil
}

// env is the fresh look at the host that a plan is made from: the config (an unreadable one is ConfigErr, a missing one
// is a host that is not set up), the mounts and the data mounts right after it, the accounts, the interfaces, and the
// Samba users of a fresh pdbedit -L -v when the extension is set up.
func (a *API) env(ctx context.Context) (actions.Env, error) {
	fsys := a.fs()
	mode, err := fsys.Mode()
	if err != nil {
		return actions.Env{}, fmt.Errorf("reading how Alpine runs: %w", err)
	}
	t := a.tools()
	cfg, set, cfgErr := samba.Load(fsys)
	mounts, err := fsys.Mounts()
	if err != nil {
		return actions.Env{}, fmt.Errorf("reading the mounts: %w", err)
	}
	data, err := paths.DataMounts(fsys, a.statfs())
	if err != nil {
		return actions.Env{}, fmt.Errorf("reading the data disks: %w", err)
	}
	db, err := accounts.Load(a.Root, a.now())
	if err != nil {
		return actions.Env{}, fmt.Errorf("reading the accounts: %w", err)
	}
	ifaces, err := a.ifaces()
	if err != nil {
		return actions.Env{}, fmt.Errorf("reading the network interfaces: %w", err)
	}
	env := actions.Env{FS: fsys, Tools: t, Mode: mode, Config: cfg, ConfigErr: cfgErr, SetUp: set, DB: db, Mounts: mounts,
		Data: data, Ifaces: ifaces, Binary: a.Binary, Self: a.Self, Probe: a.Probe, Ctx: ctx}
	if set && cfgErr == nil {
		if env.SambaUsers, err = a.sambaTools(t).Users(ctx); err != nil {
			return actions.Env{}, err
		}
	}
	return env, nil
}

// dropWait is how long an add waits at most for the job that drops a stale name: the time limits of the steps of a change
// of the config (lbu commit takes up to 2 minutes), with room.
const dropWait = 10 * time.Minute

// await waits for the end of the job id, which the caller started and still holds its copy of the job lock for: no other
// job starts meanwhile, and the lock names the job, so that it reads running while its runner works, and also after a
// runner that died. So the wait ends as soon as the runner, the process pid, is gone (gone): the job is read once more
// then, which a runner that ended well has ended (it writes the end before it goes), and one that died has not. It
// returns the job as it ended, or as it is when the runner is gone or dropWait is over.
func (a *API) await(id string, pid int) (jobs.State, error) {
	deadline := time.Now().Add(dropWait)
	for {
		st, err := a.Jobs.Get(id)
		if err != nil || st.Status != jobs.Running || time.Now().After(deadline) {
			return st, err
		}
		if gone(pid) {
			return a.Jobs.Get(id)
		}
		time.Sleep(a.poll())
	}
}

// gone reports whether the process pid has ended: kill(pid, 0) finds no such process (ESRCH). The launcher reaps its
// runners, so an ended one leaves no zombie that would still answer. A pid that is not known (0 or less) is never gone,
// and the wait ends at dropWait.
func gone(pid int) bool { return pid > 0 && errors.Is(unix.Kill(pid, 0), unix.ESRCH) }

// shownJob is the job that the view shows: the newest while it runs or is interrupted (a SIGTERM ends one failed with the
// code interrupted, which the page treats alike); a job that failed or is done shows in the answer that watches it.
func (a *API) shownJob() *jobs.State {
	newest, err := a.Jobs.Newest()
	switch {
	case err != nil:
		slog.Warn("samba: the newest job cannot be read", "err", err)
	case newest != nil && (newest.Status == jobs.Running || newest.Status == jobs.Interrupted ||
		newest.Status == jobs.Failed && newest.Code == jobs.CodeInterrupted):
		return newest
	}
	return nil
}
