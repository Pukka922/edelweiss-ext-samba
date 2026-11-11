// Package api serves the extension's HTTP API. For everybody: the view of the setup, the shares, the Samba users, the
// disks, the interfaces and the services, and the state of a job. In admin mode: the navigator of the data disks with
// its "Neuer Ordner", the folders of a share with their rights, the recycle bin of a share, the connections, the
// changes (as jobs), the cancel of a job, and the Samba users and service accounts.
//
// A change of the config is planned by internal/actions on a fresh look at the host (the Env, read under the job lock),
// and runs as a job (internal/jobs) in a detached runner, which holds the job lock to its end: one change at a time.
//
// The account routes work synchronously under the job lock. The methods of internal/accounts take the account lock of
// edelweiss-ext-users themselves. The routes take it only around their own calls of pdbedit and smbpasswd, one hold at
// a time, never around a method of internal/accounts: a second flock of the same file would wait for the first. In
// diskless and data mode every account route runs lbu commit right after its account change. The config change that
// follows the removal of a user runs as a job (user-drop). An add of a name that the config still names without a Samba
// entry waits for that job first.
//
// A password is read from the body, which is cleared afterwards. It goes to the Samba tools on stdin only, and is never
// logged, kept or put into a job.
//
// The JSON of the answers is camelCase. A list is a list, never null. Every refusal is the SDK's error envelope with a
// code (toHTTP).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss/sdk"
)

// API holds the dependencies of the routes.
//
//   - Root is the root of the system ("/" on the host): everything is read below it.
//   - Runner runs the commands of the API itself (pdbedit, smbpasswd, smbstatus, testparm, lbu).
//   - Tools looks up the tools afresh for each request.
//   - Jobs and Launcher are the store of the jobs and what starts their runner.
//   - Self is the binary that the steps of a job run as --samba-tool.
//   - Binary is the extension's binary with its links resolved: smbd's root preexec and the periodic script call it.
//
// The rest are the seams of the host, each the real one when it is nil (0):
//   - Accounts makes and removes the accounts (accounts.New on Root with the busybox of the host).
//   - Ifaces are the network interfaces (samba.SystemInterfaces), and Statfs the sizes of the disks (paths.RealStatfs).
//   - Probe is the probe of the folder rights (Env.Probe), and Now the clock.
//   - Poll is how often an add looks at the job that drops a stale name (200 ms).
//   - OwnerOf is the owner of a folder that the routes look at themselves (paths.OwnerOf), and MakeDir what makes the
//     folder of "Neuer Ordner" (paths.MakeDir): a test makes a folder of root, and gives a folder away, with them, which
//     only root can.
//   - MountsFile is the file below Root that the API reads the mounts from (host.FS.MountsFile), /proc/mounts when it
//     is "": the container test names a mount point there that is none, so that smbd's check refuses the share on it as
//     it refuses one whose disk is unplugged.
type API struct {
	Root     string
	Runner   run.Runner
	Tools    func() host.Tools
	Jobs     jobs.Store
	Launcher jobs.Launcher
	Self     string
	Binary   string

	Accounts *accounts.Service
	Ifaces   func() ([]samba.Iface, error)
	Statfs   paths.Statfs
	Probe    func(samba.Share) error
	Now      func() time.Time
	Poll     time.Duration
	OwnerOf  func(root safefs.Root, path string, db *accounts.DB) (paths.Ownership, error)
	MakeDir  func(root safefs.Root, parent, name string, perm os.FileMode, uid, gid int) error

	MountsFile string
}

// maxBody is the largest body of every route.
const maxBody = 64 << 10

// Register adds the routes to r. Every change is for the admin mode, and so are the navigator, the folders of a share,
// the recycle bin and the connections: the admin check comes before anything of the request is looked at, but for the
// body of 64 KiB at most, which limitBody has read by then. The view and the state of a job are for everybody.
//
// No request leaves a body unread on a connection that is kept: the routes have limitBody, and the last middleware has
// it for what no route answers, after which Fiber answers 404 or 405.
func Register(r fiber.Router, a *API) {
	small := limitBody(maxBody)
	admin := sdk.RequireAdmin()
	r.Get("/view", small, a.view)
	r.Get("/folders", small, admin, a.folders)
	r.Get("/browse", small, admin, a.browse)
	r.Post("/browse/mkdir", small, admin, a.mkdir)
	r.Get("/recycle", small, admin, a.recycleBin)
	r.Get("/connections", small, admin, a.connections)
	r.Post("/jobs", small, admin, a.createJob)
	r.Get("/jobs/:id", small, a.job)
	r.Post("/jobs/:id/cancel", small, admin, a.cancel)
	r.Post("/users/samba", small, admin, a.addSambaUser)
	r.Post("/users/host", small, admin, a.addHostUser)
	r.Post("/users/:name/password", small, admin, a.setPassword)
	r.Delete("/users/:name", small, admin, a.deleteUser)
	r.Post("/service-accounts", small, admin, a.serviceAccount)
	r.Use(small) // last: for what no route answers
}

// The codes of the answers that internal/actions has no code for.
const (
	codeInvalidJSON    = "invalid_json"
	codeBusy           = "busy"
	codeWrongPassword  = "wrong_password"
	codeLengthRequired = "length_required"
	codeTooLarge       = "too_large"
	codeNotCancellable = "not_cancellable"
)

// statusOf maps the codes to HTTP; every other refusal is a conflict (409).
var statusOf = map[string]int{
	actions.CodeInvalidInput: fiber.StatusBadRequest,
	codeInvalidJSON:          fiber.StatusBadRequest,
	actions.CodeNotFound:     fiber.StatusNotFound,
	actions.CodeFailed:       fiber.StatusInternalServerError,
}

// apiError is the SDK's error envelope of a code: its status, the message, and the details, with the field of an invalid
// input or of a name that exists as "field".
func apiError(code, message, field string, details map[string]any) error {
	status, ok := statusOf[code]
	if !ok {
		status = fiber.StatusConflict
	}
	e := sdk.NewError(status, code, message)
	details = maps.Clone(details)
	if (code == actions.CodeInvalidInput || code == actions.CodeExists) && field != "" {
		if details == nil {
			details = map[string]any{}
		}
		details["field"] = field
	}
	if len(details) > 0 {
		e = e.WithDetails(details)
	}
	return e
}

// toHTTP turns an error into the SDK's error envelope. A refusal of internal/actions or internal/accounts keeps its
// code, its message and its details (the group of a system_account of internal/accounts: group); a name that
// internal/accounts finds taken is exists. A job whose runner could not be
// started is failed with the job (details job). Any other error is a failure of the host (500 failed).
func toHTTP(err error) error {
	var sdkErr *sdk.APIError
	if errors.As(err, &sdkErr) {
		return sdkErr
	}
	var se *startError
	if errors.As(err, &se) {
		return refuse(actions.CodeFailed, se.msg, map[string]any{"job": se.job})
	}
	var ae *actions.Error
	if errors.As(err, &ae) {
		return apiError(ae.Code, ae.Message, ae.Field, ae.Details)
	}
	var ce *accounts.Error
	if errors.As(err, &ce) {
		code := ce.Code
		if code == accounts.CodeTaken {
			code = actions.CodeExists
		}
		var details map[string]any
		if ce.Group != "" {
			details = map[string]any{"group": ce.Group}
		}
		return apiError(code, ce.Message, ce.Field, details)
	}
	return sdk.NewError(fiber.StatusInternalServerError, actions.CodeFailed, err.Error())
}

// refuse is the answer for a refusal with the code.
func refuse(code, message string, details map[string]any) error {
	return apiError(code, message, "", details)
}

// invalid is the answer to an invalid input in the field of the request.
func invalid(field, format string, a ...any) error {
	return apiError(actions.CodeInvalidInput, fmt.Sprintf(format, a...), field, nil)
}

// invalidFor is invalid with the reason that the page words.
func invalidFor(field, reason, message string) error {
	return apiError(actions.CodeInvalidInput, message, field, map[string]any{"reason": reason})
}

func notFound(details map[string]any, format string, a ...any) error {
	return refuse(actions.CodeNotFound, fmt.Sprintf(format, a...), details)
}

func failed(format string, a ...any) error {
	return refuse(actions.CodeFailed, fmt.Sprintf(format, a...), nil)
}

// decode reads the JSON body into v and then clears the body: a password in it must not stay in the buffer of the request,
// which the server uses again. A string that json made of it is a copy, and stays valid.
func decode(c fiber.Ctx, v any) error {
	body := c.BodyRaw()
	err := json.Unmarshal(body, v)
	clear(body)
	if err != nil {
		return sdk.NewError(fiber.StatusBadRequest, codeInvalidJSON, "the request body must be valid JSON")
	}
	return nil
}

// param is the user name of the route, percent-decoded exactly once (the UI sends encodeURIComponent(name)).
func param(c fiber.Ctx) (string, error) {
	name, err := url.PathUnescape(c.Params("name"))
	if err != nil {
		return "", invalid("name", "the name in the path is not valid percent-encoding")
	}
	return name, nil
}

// chunked is the length that fasthttp gives a body that is sent in chunks: it has no length.
const chunked = -1

// drainMax is the most that is read of a body that is refused for its size, so that the client gets the answer.
const drainMax = 1 << 20

// limitBody refuses a request whose body is longer than max (413 too_large) or has no length (411 length_required), going by
// the header. A body within the limit is read here, to its end, so that what a later refusal leaves unread is never read
// as the head of the next request; so is a body that is refused for its size, up to drainMax. The connection of a longer
// body, or of one without a length, ends with the answer.
func limitBody(max int) fiber.Handler {
	return func(c fiber.Ctx) error {
		n := c.Request().Header.ContentLength()
		switch {
		case n == chunked:
			c.RequestCtx().SetConnectionClose()
			return sdk.NewError(fiber.StatusLengthRequired, codeLengthRequired, "the request needs a Content-Length")
		case n > max:
			if n <= drainMax {
				_ = c.BodyRaw()
			} else {
				c.RequestCtx().SetConnectionClose()
			}
			return sdk.NewError(fiber.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("the request body is larger than %d bytes", max))
		}
		_ = c.BodyRaw()
		return c.Next()
	}
}

// ctxOf is the context a request runs under. With Fiber v3.5, c.Context() is context.Background() unless one is set,
// and fasthttp gives no signal when the client leaves: a command is bounded by its own time limit. Do not use
// c.RequestCtx() instead: it ends only at the shutdown of the server, and would kill a command halfway.
func ctxOf(c fiber.Ctx) context.Context { return c.Context() }

func (a *API) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

func (a *API) fs() host.FS { return host.FS{Root: a.Root, MountsFile: a.MountsFile} }

func (a *API) tools() host.Tools {
	if a.Tools == nil {
		return host.FindTools(a.Root)
	}
	return a.Tools()
}

func (a *API) sambaTools(t host.Tools) samba.Tools { return samba.Tools{R: a.Runner, T: t} }

func (a *API) service() *accounts.Service {
	if a.Accounts == nil {
		return accounts.New(a.Root, busybox.Exec{Path: busybox.Path, Env: busybox.Env})
	}
	return a.Accounts
}

func (a *API) ifaces() ([]samba.Iface, error) {
	if a.Ifaces == nil {
		return samba.SystemInterfaces()
	}
	return a.Ifaces()
}

func (a *API) statfs() paths.Statfs {
	if a.Statfs == nil {
		return paths.RealStatfs
	}
	return a.Statfs
}

// ownerOf is the owner of the folder (OwnerOf, or paths.OwnerOf).
func (a *API) ownerOf(root safefs.Root, path string, db *accounts.DB) (paths.Ownership, error) {
	if a.OwnerOf == nil {
		return paths.OwnerOf(root, path, db)
	}
	return a.OwnerOf(root, path, db)
}

// makeDir makes the folder of "Neuer Ordner" (MakeDir, or paths.MakeDir).
func (a *API) makeDir(root safefs.Root, parent, name string, perm os.FileMode, uid, gid int) error {
	if a.MakeDir == nil {
		return paths.MakeDir(root, parent, name, perm, uid, gid)
	}
	return a.MakeDir(root, parent, name, perm, uid, gid)
}

func (a *API) poll() time.Duration {
	if a.Poll <= 0 {
		return 200 * time.Millisecond
	}
	return a.Poll
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func orEmptyMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}
