package actions

import (
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The narrowing rule of the folder rights, the one place it is: the plans write what it gives (apply-acl, the folder
// rights of the config), and the API shows it. Folder rights only narrow.

// Effective are the rights in the folder below the share ("" is the share's folder): the share's, narrowed by the own
// rights of every folder on the way to it and by its own (samba.Narrow); users without a right are not there.
func Effective(s samba.Share, folder string) map[string]samba.Right {
	rights := cleanRights(s.Rights)
	if folder == "" {
		return rights
	}
	parts := strings.Split(folder, "/")
	for n := range parts {
		own, ok := s.Folders[strings.Join(parts[:n+1], "/")]
		if !ok {
			continue
		}
		for u, r := range rights {
			if nr := samba.Narrow(r, own[u]); nr != "" {
				rights[u] = nr
			} else {
				delete(rights, u)
			}
		}
	}
	return rights
}

// ParentFolder is the folder above f: "" for a folder in the share's folder, and for the share's folder itself.
func ParentFolder(f string) string {
	if i := strings.LastIndexByte(f, '/'); i >= 0 {
		return f[:i]
	}
	return ""
}

// RightsFrom is the folder whose rights f has: f itself when it has rights of its own, else the nearest folder above it
// that has, else "" (the share's folder).
func RightsFrom(s samba.Share, f string) string {
	for ; f != ""; f = ParentFolder(f) {
		if _, ok := s.Folders[f]; ok {
			return f
		}
	}
	return ""
}
