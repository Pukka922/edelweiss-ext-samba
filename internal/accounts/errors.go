package accounts

import (
	"errors"
	"fmt"

	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/etcfile"
)

// The codes of the refusals and failures; the API maps them to HTTP statuses.
const (
	CodeInvalid  = "invalid_input"
	CodeTaken    = "name_taken"
	CodeNotFound = "not_found"
	CodeSystem   = "system_account"
	CodeBusy     = "busy"
	CodeFailed   = "failed"
)

const busyMessage = "another program is changing the account files; try again"

// Error is a refusal or a failure with a stable code. Field names the input of an invalid_input or a name_taken; Group
// names the group of the system that a system_account of a service account's GID is about.
type Error struct {
	Code    string
	Message string
	Field   string
	Group   string
}

func (e *Error) Error() string { return e.Message }

func refuse(code, field, format string, args ...any) *Error {
	return &Error{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}

// fromRun turns an error of busybox or of the file writer into an *Error: busy when another program holds a lock,
// failed otherwise.
func fromRun(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var bb *busybox.Error
	if errors.As(err, &bb) && bb.Busy() || errors.Is(err, etcfile.ErrBusy) {
		return &Error{Code: CodeBusy, Message: busyMessage}
	}
	return &Error{Code: CodeFailed, Message: err.Error()}
}
