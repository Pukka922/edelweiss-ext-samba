package samba

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// SambaUser is an entry of Samba's user database.
type SambaUser struct {
	Name     string
	SID      string
	Disabled bool
}

// Tools runs the Samba programs that manage the user database.
type Tools struct {
	R run.Runner
	T host.Tools
}

// ErrPassword is returned for a password that cannot be handed to Samba (accounts.ValidPassword: empty, longer than 256
// bytes, or with a line break or NUL: the password goes on one line of stdin). The error never holds the password.
var ErrPassword = errors.New("invalid password")

// ErrName is returned for a user name that accounts.ValidName refuses, which also keeps a name from being an option.
var ErrName = errors.New("invalid user name")

// Users lists the Samba users: pdbedit -L -v, parsed.
func (s Tools) Users(ctx context.Context) ([]SambaUser, error) {
	out, err := s.R.Run(ctx, run.Cmd{Path: s.T.Pdbedit, Args: []string{"-L", "-v"}})
	if err != nil {
		return nil, fmt.Errorf("list Samba users: %w", err)
	}
	return parseUsers(out), nil
}

// parseUsers reads the blocks of pdbedit -L -v: "Unix username:" starts one, "Account Flags:" has D for a disabled
// account, "User SID:" the SID.
func parseUsers(out string) []SambaUser {
	users := []SambaUser{}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Unix username":
			users = append(users, SambaUser{Name: val})
		case "Account Flags":
			if n := len(users); n > 0 {
				users[n-1].Disabled = strings.Contains(strings.TrimSuffix(strings.TrimPrefix(val, "["), "]"), "D")
			}
		case "User SID":
			if n := len(users); n > 0 {
				users[n-1].SID = val
			}
		}
	}
	return users
}

// stdinPassword is what the tools read: the password twice, one per line.
func stdinPassword(password string) string { return password + "\n" + password + "\n" }

// Add makes the Samba entry of an existing Unix user, or sets the password of an entry that exists (the SID stays):
// pdbedit -a -u name -t with the password twice on stdin.
func (s Tools) Add(ctx context.Context, name, password string) error {
	if !accounts.ValidName(name) {
		return ErrName
	}
	if !accounts.ValidPassword(password) {
		return ErrPassword
	}
	_, err := s.R.Run(ctx, run.Cmd{Path: s.T.Pdbedit, Args: []string{"-a", "-u", name, "-t"}, Stdin: stdinPassword(password)})
	if err != nil {
		return fmt.Errorf("add Samba user %s: %w", name, err)
	}
	return nil
}

// SetPassword sets the password of a Samba user: smbpasswd -s name with the password twice on stdin.
func (s Tools) SetPassword(ctx context.Context, name, password string) error {
	if !accounts.ValidName(name) {
		return ErrName
	}
	if !accounts.ValidPassword(password) {
		return ErrPassword
	}
	_, err := s.R.Run(ctx, run.Cmd{Path: s.T.Smbpasswd, Args: []string{"-s", name}, Stdin: stdinPassword(password)})
	if err != nil {
		return fmt.Errorf("set the Samba password of %s: %w", name, err)
	}
	return nil
}

// Remove deletes the Samba entry of a user: pdbedit -x name.
func (s Tools) Remove(ctx context.Context, name string) error {
	if !accounts.ValidName(name) {
		return ErrName
	}
	if _, err := s.R.Run(ctx, run.Cmd{Path: s.T.Pdbedit, Args: []string{"-x", name}}); err != nil {
		return fmt.Errorf("remove Samba user %s: %w", name, err)
	}
	return nil
}

// Enable enables (smbpasswd -e) or disables (smbpasswd -d) a Samba user.
func (s Tools) Enable(ctx context.Context, name string, on bool) error {
	if !accounts.ValidName(name) {
		return ErrName
	}
	flag, verb := "-d", "disable"
	if on {
		flag, verb = "-e", "enable"
	}
	if _, err := s.R.Run(ctx, run.Cmd{Path: s.T.Smbpasswd, Args: []string{flag, name}}); err != nil {
		return fmt.Errorf("%s Samba user %s: %w", verb, name, err)
	}
	return nil
}
