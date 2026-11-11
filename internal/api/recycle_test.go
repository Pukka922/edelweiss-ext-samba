package api

import (
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The recycle bin of a share is for the admin mode; it is listed only where the disk of the share is there.
func TestRecycleNeedsAdmin(t *testing.T) {
	f := newHarness(t)
	fotos := f.fotos()
	fotos.Recycle, fotos.RecycleDays = true, 30
	alt := samba.Share{Name: "Alt", Path: "/mnt/gone/alt", Mount: "/mnt/gone", Fsid: "ext4:1", Recycle: true, RecycleDays: 30}
	f.config(fotos, alt)
	f.write("/mnt/disk/data/#recycle/sub/a.txt", "12345")
	f.write("/mnt/gone/alt/#recycle/b.txt", "678")

	f.send("GET", "/recycle?share=Fotos", nil, plain).wantError(t, 403, "admin_required")
	got := decodeAs[RecycleResponse](t, f.send("GET", "/recycle?share=Fotos", nil, admin).want(t, 200))
	if got.Share != "Fotos" || len(got.Items) != 1 || got.Items[0].Path != "sub/a.txt" || got.Items[0].Size != 5 ||
		got.Items[0].Deleted.IsZero() || got.Summary != (SummaryView{Files: 1, Bytes: 5}) || got.Truncated {
		t.Errorf("recycle %+v", got)
	}
	if e := f.send("GET", "/recycle?share=Alt", nil, admin).wantError(t, 409, "unavailable"); e["share"] != "Alt" {
		t.Errorf("unplugged: %v", e)
	}
	f.send("GET", "/recycle?share=Nope", nil, admin).wantError(t, 404, "not_found")
}

// A bin with more files than the list holds says that the list is cut (truncated), and its numbers stay exact: partial is
// only a count that did not see the whole bin.
func TestRecycleTruncated(t *testing.T) {
	f := newHarness(t)
	fotos := f.fotos()
	fotos.Recycle, fotos.RecycleDays = true, 30
	f.config(fotos)
	f.write("/mnt/disk/data/#recycle/a.txt", "12345")
	f.write("/mnt/disk/data/#recycle/b.txt", "678")
	old := maxRecycleItems
	maxRecycleItems = 1
	defer func() { maxRecycleItems = old }()
	got := decodeAs[RecycleResponse](t, f.send("GET", "/recycle?share=Fotos", nil, admin).want(t, 200))
	if len(got.Items) != 1 || !got.Truncated || got.Summary != (SummaryView{Files: 2, Bytes: 8}) {
		t.Errorf("cut: %+v", got)
	}
	maxRecycleItems = 2
	got = decodeAs[RecycleResponse](t, f.send("GET", "/recycle?share=Fotos", nil, admin).want(t, 200))
	if len(got.Items) != 2 || got.Truncated || got.Summary != (SummaryView{Files: 2, Bytes: 8}) {
		t.Errorf("whole: %+v", got)
	}
}
