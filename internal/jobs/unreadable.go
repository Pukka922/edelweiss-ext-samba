package jobs

import (
	"errors"
	"io/fs"
)

// ErrUnreadable means that the state of a job cannot be read: the file is not valid JSON, or cannot be read.
var ErrUnreadable = errors.New("jobs: the state of a job cannot be read")

// Running is every job that runs, as Get reads them, newest first: the job that holds the job lock. A job that is removed
// meanwhile is none, and so is a job whose state cannot be read (Unreadable says whether the lock names it): what is known of such a job is its id, which is no
// job to show, and no use to compare.
func (s Store) Running() ([]State, error) {
	var out []State
	for _, id := range s.ids() {
		st, err := s.Get(id)
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrUnreadable):
		case err != nil:
			return nil, err
		case st.Status == Running:
			out = append(out, st)
		}
	}
	return out, nil
}

// Unreadable is a job whose state cannot be read. Held says that the held job lock names the job: it may be running, as the state that
// was written last said, and nothing is known of what it holds. Otherwise the job is dead, or was never started, and its directory is of no use: Create removes it
// like any old job.
type Unreadable struct {
	ID   string
	Held bool
	Err  error
}

// Unreadable lists the jobs whose state cannot be read, newest first.
func (s Store) Unreadable() []Unreadable {
	var out []Unreadable
	for _, id := range s.ids() {
		if _, err := s.read(id); errors.Is(err, ErrUnreadable) {
			out = append(out, Unreadable{ID: id, Held: s.holder() == id, Err: err})
		}
	}
	return out
}
