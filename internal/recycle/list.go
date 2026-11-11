// Package recycle is the recycle bin of a share: the folder #recycle that vfs_recycle moves deleted files into (with
// recycle:keeptree the path below the share is kept). The extension lists it, restores from it, empties it, and the daily
// cleanup removes what is older than the share's retention.
//
// Everything is done through descriptors (package safefs): the share folder is reached without a link, every folder below
// it is opened from the one above, one name at a time, and a symbolic link anywhere is refused, so that a link that
// someone planted on the disk leads nowhere. A walk stays on the file system and the mount of #recycle.
//
// The time a file was deleted is its change time (ctime): the move into #recycle sets it, and a read or a listing leaves it
// as it is. The access time is no deletion time: under relatime a read moves it. A client that changes the DOS attributes
// of a file in #recycle, or any extended attribute written to it, moves the ctime as well, and with it the day the cleanup
// removes the file.
package recycle

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// Dir is the folder of the recycle bin, below the share's folder.
const Dir = "#recycle"

// MaxItems is the limit that the recycle bin view lists at most.
const MaxItems = 5000

// maxScan is how many entries List looks at before it gives up (Partial): the bin is walked whole, but not without end.
var maxScan = 200000

// Item is a file in the recycle bin. Path is relative to the share's folder, as before the deletion (the path below
// #recycle); Deleted is its change time.
type Item struct {
	Path    string
	Size    int64
	Deleted time.Time
}

// Summary is the number of files and bytes that were counted. Partial: the numbers are not the whole bin (List: the walk had
// to stop or skipped an entry; Sum: the time budget ended first). A list that holds fewer items than Files is cut at its
// limit; that alone leaves the numbers exact.
type Summary struct {
	Files   int
	Bytes   int64
	Partial bool
}

// older orders items by deletion time, and by path (the later path is the older one) when the times are the same.
func older(a, b Item) bool {
	if !a.Deleted.Equal(b.Deleted) {
		return a.Deleted.Before(b.Deleted)
	}
	return a.Path > b.Path
}

// oldestFirst is a heap with the oldest item at its root.
type oldestFirst []Item

func (h oldestFirst) Len() int           { return len(h) }
func (h oldestFirst) Less(i, j int) bool { return older(h[i], h[j]) }
func (h oldestFirst) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *oldestFirst) Push(x any)        { *h = append(*h, x.(Item)) }
func (h *oldestFirst) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// List returns the newest limit files in the recycle bin of the share (the absolute path of its folder), newest deletion
// first. The bin is walked whole (up to maxScan entries), and only limit items are kept in memory. Summary has the number
// and size of all the files that were seen, and Partial when the walk stopped at maxScan or an entry could not be looked
// at; more files than the limit cut the list, not the numbers. Links are not listed, and not entered. A share without
// #recycle has no files.
func List(root safefs.Root, share string, limit int) ([]Item, Summary, error) {
	if limit < 1 {
		return nil, Summary{}, fmt.Errorf("the limit %d is below 1", limit)
	}
	fd, ok, err := openRecycle(root, share)
	if err != nil || !ok {
		return nil, Summary{}, err
	}
	defer unix.Close(fd)
	var keep oldestFirst
	var sum Summary
	scanned := 0
	capped := false
	f, err := walkBin(context.Background(), fd, func(e walk.Entry) error {
		if scanned++; scanned > maxScan {
			capped = true
			return walk.ErrStop
		}
		if !isRegular(&e.Stat) {
			return nil
		}
		sum.Files++
		sum.Bytes += e.Stat.Size
		it := Item{Path: e.Rel, Size: e.Stat.Size, Deleted: ctimeOf(&e.Stat)}
		switch {
		case len(keep) < limit:
			heap.Push(&keep, it)
		case older(keep[0], it):
			keep[0] = it
			heap.Fix(&keep, 0)
		}
		return nil
	}, nil, nil)
	if err != nil {
		return nil, Summary{}, err
	}
	sum.Partial = capped || f.N > 0
	items := []Item(keep)
	sort.Slice(items, func(i, j int) bool { return older(items[j], items[i]) })
	return items, sum, nil
}

// Sum counts the files and bytes in the recycle bin of the share. It stops when the time budget has ended: Partial is then
// set and the numbers are what was counted by then (also set when an entry could not be looked at).
func Sum(root safefs.Root, share string, budget time.Duration) (Summary, error) {
	fd, ok, err := openRecycle(root, share)
	if err != nil || !ok {
		return Summary{}, err
	}
	defer unix.Close(fd)
	deadline := time.Now().Add(budget)
	var sum Summary
	f, err := walkBin(context.Background(), fd, func(e walk.Entry) error {
		if isRegular(&e.Stat) {
			sum.Files++
			sum.Bytes += e.Stat.Size
		}
		return nil
	}, nil, func() bool {
		if time.Now().Before(deadline) {
			return false
		}
		sum.Partial = true
		return true
	})
	if err != nil {
		return Summary{}, err
	}
	if f.N > 0 {
		sum.Partial = true
	}
	return sum, nil
}
