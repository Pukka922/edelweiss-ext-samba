package busybox

import (
	"context"
	"strings"
	"sync"
)

// Call is one call of a Fake.
type Call struct {
	Args  []string
	Stdin string
}

// Fake is a Runner for tests: it records every call, fails a call whose args joined by spaces are a key of Fail, and
// otherwise runs OnRun (if set), which may change a test root the way busybox would.
type Fake struct {
	mu    sync.Mutex
	Calls []Call
	Fail  map[string]error
	OnRun func(args []string) error
}

// Run records the call.
func (f *Fake) Run(_ context.Context, stdin string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, Call{Args: args, Stdin: stdin})
	if err, ok := f.Fail[strings.Join(args, " ")]; ok {
		return err
	}
	if f.OnRun != nil {
		return f.OnRun(args)
	}
	return nil
}

// Commands are the recorded calls, each as its args joined by spaces.
func (f *Fake) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.Calls {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}
