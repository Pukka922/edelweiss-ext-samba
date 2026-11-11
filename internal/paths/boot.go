package paths

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
)

// BootMedia are the mount points below /media/ of the media that Alpine runs from (bootMedia: the LBU_MEDIA of lbu.conf,
// a medium that holds apks/ or a *.apkovl.tar.gz, the medium of a loop device's backing file). It reads the host, and
// excludes nothing: WarningsFor gives a folder on such a medium the system warning.
func BootMedia(fs host.FS, mounts []host.Mount) []string {
	return bootMedia(fs, mounts, loopBackings(fs))
}

// lbuMedia matches the LBU_MEDIA assignment of lbu.conf: in double quotes, in single quotes or bare.
var lbuMedia = regexp.MustCompile(`^\s*LBU_MEDIA=(?:"([^"]*)"|'([^']*)'|([^\s#"']*))`)

func belowMedia(p string) bool { return strings.HasPrefix(p, "/media/") && path.Clean(p) == p }

// loopBackings are the backing files of the attached loop devices (the modloop of the boot medium is one), from
// /sys/block/loop*/loop/backing_file.
func loopBackings(fs host.FS) []string {
	files, _ := filepath.Glob(filepath.Join(fs.Root, "sys/block/loop*/loop/backing_file"))
	var out []string
	for _, f := range files {
		if data, err := os.ReadFile(f); err == nil {
			if p := strings.TrimSuffix(string(data), "\n"); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// bootMedia are the mount points below /media/ of the media that Alpine runs from: the LBU_MEDIA of etc/lbu/lbu.conf as
// /media/<x>, every mount below /media/ whose directory holds apks/ or a *.apkovl.tar.gz, and the mount that holds the
// backing file of an attached loop device (loops).
func bootMedia(fs host.FS, mounts []host.Mount, loops []string) []string {
	var media []string
	for _, m := range mounts {
		if belowMedia(m.Point) {
			media = append(media, m.Point)
		}
	}
	var out []string
	if lbu, ok := lbuMediaPoint(fs); ok && slices.Contains(media, lbu) {
		out = append(out, lbu)
	}
	for _, p := range media {
		if holdsBootFiles(fs, p) {
			out = append(out, p)
		}
	}
	for _, backing := range loops {
		holder := "" // the innermost mount that holds the file
		for _, p := range media {
			if strings.HasPrefix(backing, p+"/") && len(p) > len(holder) {
				holder = p
			}
		}
		if holder != "" {
			out = append(out, holder)
		}
	}
	return out
}

// lbuMediaPoint is the mount point /media/<x> of the LBU_MEDIA=<x> of etc/lbu/lbu.conf (the last assignment counts, as in
// the shell).
func lbuMediaPoint(fs host.FS) (string, bool) {
	data, err := os.ReadFile(filepath.Join(fs.Root, "etc/lbu/lbu.conf"))
	if err != nil {
		return "", false
	}
	var name string
	for _, line := range strings.Split(string(data), "\n") {
		if m := lbuMedia.FindStringSubmatch(line); m != nil {
			name = m[1] + m[2] + m[3]
		}
	}
	if name == "" {
		return "", false
	}
	point := path.Join("/media", name)
	return point, belowMedia(point)
}

// holdsBootFiles reports whether the directory of a mount holds apks/ or a *.apkovl.tar.gz, the files of an Alpine boot
// medium.
func holdsBootFiles(fs host.FS, point string) bool {
	if st, err := os.Stat(filepath.Join(fs.Root, point, "apks")); err == nil && st.IsDir() {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(fs.Root, point))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".apkovl.tar.gz") {
			return true
		}
	}
	return false
}
