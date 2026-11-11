package api

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// RecycleResponse is the answer of GET /recycle: the newest files in the recycle bin of the share (at most
// maxRecycleItems), newest first, and the number and size of all that were seen. Truncated: the bin holds more files than
// the list; Summary.Partial: the count did not see the whole bin (the numbers are a minimum).
type RecycleResponse struct {
	Share     string         `json:"share"`
	Items     []RecycleEntry `json:"items"`
	Truncated bool           `json:"truncated"`
	Summary   SummaryView    `json:"summary"`
}

// maxRecycleItems is the most files that GET /recycle lists (recycle.MaxItems; a test lowers it).
var maxRecycleItems = recycle.MaxItems

// RecycleEntry is a file in the recycle bin: its path below the share as before the deletion (what recycle-restore takes
// as the item), its size, and when it was deleted.
type RecycleEntry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Deleted time.Time `json:"deleted"`
}

// recycleBin answers GET /recycle?share= (admin mode): the recycle bin of a share whose disk is there (paths.Available, else
// unavailable: nothing is read on the mount point of a disk that is unplugged).
func (a *API) recycleBin(c fiber.Ctx) error {
	name := strings.Clone(c.Query("share"))
	cfg, err := a.setUpConfig()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(cfg.Shares, func(s samba.Share) bool { return s.Name == name })
	if i < 0 {
		return notFound(map[string]any{"share": name}, "there is no share %q", name)
	}
	sh := cfg.Shares[i]
	if ok, _ := paths.Available(a.fs(), sh); !ok {
		return refuse(actions.CodeUnavailable, fmt.Sprintf("the disk of the share %q is not there", sh.Name), map[string]any{"share": sh.Name})
	}
	root, err := safefs.Open(a.Root)
	if err != nil {
		return failed("%v", err)
	}
	defer root.Close()
	items, sum, err := recycle.List(root.OnDisk(sh.Fsid), sh.Path, maxRecycleItems)
	if err != nil {
		return failed("reading the recycle bin of %s: %v", sh.Name, err)
	}
	resp := RecycleResponse{Share: sh.Name, Items: make([]RecycleEntry, 0, len(items)), Truncated: len(items) < sum.Files,
		Summary: SummaryView{Files: sum.Files, Bytes: sum.Bytes, Partial: sum.Partial}}
	for _, it := range items {
		resp.Items = append(resp.Items, RecycleEntry{Path: it.Path, Size: it.Size, Deleted: it.Deleted})
	}
	return c.JSON(resp)
}
