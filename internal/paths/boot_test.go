package paths_test

import (
	"reflect"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
)

// The medium that Alpine runs from is no hard exclusion: a folder on it, or one that holds it, gets the system warning
// (WarningsFor), whoever owns the folder (a vfat medium mounted with uid=1000 belongs to 1000). The medium is told by the
// LBU_MEDIA of lbu.conf, by the files it holds (apks/, *.apkovl.tar.gz), and by the backing file of a loop device (the
// modloop).
func TestWarningsBootMedia(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root,
		"/dev/sda1 / ext4 rw 0 0",
		"/dev/sdi1 /media/usb vfat rw,uid=1000 0 0",
		"/dev/sdj1 /media/stick vfat rw,uid=1000 0 0",
		"/dev/sdk1 /media/sdk1 vfat rw,uid=1000 0 0",
		"/dev/sdb1 /media/sdb1 ext4 rw 0 0",
		"/dev/sdl1 /media/photos ext4 rw 0 0",
		"/dev/sdm1 /mnt/data ext4 rw 0 0",
	)
	write(t, root, "media/usb/apks/x86_64/APKINDEX", "")        // boot files
	write(t, root, "media/stick/edel.apkovl.tar.gz", "")        // the overlay of lbu
	write(t, root, "media/sdk1/data.txt", "")                   // a plain medium that LBU_MEDIA names
	write(t, root, "etc/lbu/lbu.conf", "# c\nLBU_MEDIA=sdk1\n") // the last assignment counts
	write(t, root, "sys/block/loop0/loop/backing_file", "/media/sdb1/boot/modloop-lts\n")
	write(t, root, "sys/block/loop1/loop/backing_file", "\n") // a loop device without a file
	write(t, root, "media/photos/pic.jpg", "")
	ms, err := fs.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	system := []string{"system"}
	for _, c := range []struct {
		path string
		want []string
	}{
		{"/media/usb", system},
		{"/media/usb/files", system},
		{"/media/stick/x", system},
		{"/media/sdk1", system},
		{"/media/sdb1/data", system},
		{"/media/photos", nil},
		{"/media/photos/x", nil},
		{"/mnt/data", nil},
		{"/media", []string{"system", "holds_mount"}}, // holds the media
		{"/mnt", []string{"holds_mount"}},
	} {
		if got := paths.WarningsFor(fs, ms, c.path, 1000); !reflect.DeepEqual(got, c.want) {
			t.Errorf("WarningsFor(%s, 1000) = %v, want %v", c.path, got, c.want)
		}
	}
	// without a medium that holds boot files nothing changes: Warnings alone never reads the host
	if got := paths.Warnings(ms, "/media/usb", 1000); got != nil {
		t.Errorf("Warnings(/media/usb) = %v, want none (it is the pure rule of the mount table)", got)
	}
}
