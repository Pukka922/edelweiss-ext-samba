package tool

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
)

// A restore that the file system cannot do without overwriting ends with recycle.ExitNoNoreplace, which the job maps to
// the code that the page words; any other failure of a restore ends with StatusFailed.
func TestRestoreErrorStatus(t *testing.T) {
	var stderr bytes.Buffer
	tl := Tool{Stderr: &stderr}
	if got := tl.report("recycle-restore", restoreError("Fotos", fmt.Errorf("/srv/fotos/dir: %w", recycle.ErrNoNoreplace))); got != recycle.ExitNoNoreplace {
		t.Errorf("no_noreplace: status %d, want %d", got, recycle.ExitNoNoreplace)
	}
	if !strings.HasPrefix(stderr.String(), "recycle-restore: Fotos: /srv/fotos/dir: ") {
		t.Errorf("stderr %q", stderr.String())
	}
	if got := tl.report("recycle-restore", restoreError("Fotos", errors.New("boom"))); got != StatusFailed {
		t.Errorf("another failure: status %d", got)
	}
}
