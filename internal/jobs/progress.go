package jobs

import (
	"bytes"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Progress is how far the running step of a job is: the label of the step and the percent its command reported.
type Progress struct {
	Label   string  `json:"label"`
	Percent float64 `json:"percent"`
}

// defaultProgressEvery is the least time between two writes of the progress into the state, unless
// RunnerConfig.ProgressEvery says otherwise.
const defaultProgressEvery = 2 * time.Second

// progressReport is a report of a tool that prints its progress as "    (12.34/100%)", one after the other, separated by carriage
// returns.
var progressReport = regexp.MustCompile(`\((\d{1,3}(?:\.\d+)?)/100%\)`)

// maxPending is how much of the output progress keeps after the last report: enough for the start of the next one.
const maxPending = 32

// progress reads the output of a command as it comes (its write is the command's run.Cmd.Output) for the newest
// report. The runner takes it from there (take); the two run in goroutines of their own.
type progress struct {
	mu      sync.Mutex
	pending []byte // the output after the last report: the start of the next one, if it came in two pieces
	percent float64
	fresh   bool // a report that take has not given yet
}

func (p *progress) write(chunk []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append(p.pending, chunk...)
	if all := progressReport.FindAllSubmatchIndex(out, -1); len(all) > 0 {
		last := all[len(all)-1]
		if v, err := strconv.ParseFloat(string(out[last[2]:last[3]]), 64); err == nil && v <= 100 {
			p.percent, p.fresh = v, true
		}
		out = out[last[1]:]
	}
	if len(out) > maxPending {
		out = out[len(out)-maxPending:]
	}
	p.pending = bytes.Clone(out)
}

// take is the newest report and true, when there is one that it has not given yet.
func (p *progress) take() (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fresh := p.fresh
	p.fresh = false
	return p.percent, fresh
}

// report writes the newest report of the command of the Progress step s into the state, when there is a new one and
// the last write (at) is ProgressEvery ago; it returns the time of the last write.
func (r *runner) report(s Step, p *progress, at time.Time) time.Time {
	if time.Since(at) < r.cfg.ProgressEvery {
		return at
	}
	percent, fresh := p.take()
	if !fresh {
		return at
	}
	r.st.Progress = &Progress{Label: s.Label, Percent: percent}
	r.save()
	return time.Now()
}
