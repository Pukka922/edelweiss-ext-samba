// Package busybox runs the account applets of busybox: adduser, deluser, addgroup, delgroup, chpasswd and passwd. It
// always calls /bin/busybox with the applet as the first argument, so a shadow package that replaced some of the
// commands changes nothing; without a shell, with a fixed environment and "/" as the working directory. What goes to
// stdin (a password) never shows in an error.
package busybox

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Path is busybox on Alpine.
const Path = "/bin/busybox"

// Env is the whole environment of the applets.
var Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}

// Runner runs busybox with args; stdin is the applet's input.
type Runner interface {
	Run(ctx context.Context, stdin string, args ...string) error
}

// Error is a failed applet: its arguments, its output (stdout and stderr, trimmed, without the stdin text) and the
// error of the process.
type Error struct {
	Args   []string
	Output string
	Err    error
}

func (e *Error) Error() string {
	msg := "busybox " + strings.Join(e.Args, " ") + ": " + e.Err.Error()
	if e.Output != "" {
		msg += ": " + e.Output
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Busy reports whether busybox gave up because another program is changing the file: update_passwd() could not create
// the <file>+ that is the lock ("can't create '/etc/group+': File exists"). Another reason that names a <file>+ (a
// read-only file system, a missing permission) is a failure, not a busy file: trying again changes nothing.
func (e *Error) Busy() bool { return strings.Contains(e.Output, "+': File exists") }

// Exec runs the busybox at Path. Timeout limits each call (60 s if 0).
type Exec struct {
	Path    string
	Env     []string
	Timeout time.Duration
}

// Run runs one applet.
func (x Exec) Run(ctx context.Context, stdin string, args ...string) error {
	timeout := x.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, x.Path, args...)
	cmd.Env = x.Env
	cmd.Dir = "/"
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	output := string(out)
	if s := strings.TrimSuffix(stdin, "\n"); s != "" {
		output = strings.ReplaceAll(output, s, "…") // an applet that echoes its input line does not leak a password
	}
	return &Error{Args: args, Output: strings.TrimSpace(output), Err: err}
}

// AddUser creates a user with its own group and its home directory /home/<name> (with /etc/skel), without a password.
func AddUser(name, gecos, shell string) []string {
	return []string{"adduser", "-D", "-g", gecos, "-s", shell, "-h", "/home/" + name, name}
}

// DelUser deletes a user, with removeHome also its home directory.
func DelUser(name string, removeHome bool) []string {
	if removeHome {
		return []string{"deluser", "--remove-home", name}
	}
	return []string{"deluser", name}
}

// AddGroup creates a group.
func AddGroup(name string) []string { return []string{"addgroup", name} }

// DelGroup deletes a group.
func DelGroup(name string) []string { return []string{"delgroup", name} }

// AddMember adds a user to a group.
func AddMember(user, group string) []string { return []string{"addgroup", user, group} }

// RemoveMember removes a user from a group.
func RemoveMember(user, group string) []string { return []string{"delgroup", user, group} }

// ChPasswd sets passwords from stdin (ChPasswdInput), hashed with sha512.
func ChPasswd() []string { return []string{"chpasswd", "-c", "sha512"} }

// ChPasswdInput is the stdin of ChPasswd for one user.
func ChPasswdInput(name, password string) string { return name + ":" + password + "\n" }

// Lock locks the password of a user (a "!" before the hash).
func Lock(name string) []string { return []string{"passwd", "-l", name} }

// Unlock unlocks the password of a user.
func Unlock(name string) []string { return []string{"passwd", "-u", name} }
