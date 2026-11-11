package samba

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// Connection is a client connected to a share, from smbstatus --json: one per share connection (tcon). Files are the
// files that its smbd process holds open for its user, relative to the share.
type Connection struct {
	User, Machine, Address, Protocol, Share string
	Since                                   time.Time
	Files                                   []string
}

type statusServerID struct {
	PID string `json:"pid"`
}

type statusOpenFile struct {
	ServicePath string `json:"service_path"`
	Filename    string `json:"filename"`
	Opens       map[string]struct {
		ServerID statusServerID `json:"server_id"`
	} `json:"opens"`
}

type statusJSON struct {
	Sessions map[string]struct {
		ServerID      statusServerID `json:"server_id"`
		Username      string         `json:"username"`
		RemoteMachine string         `json:"remote_machine"`
		Hostname      string         `json:"hostname"`
		Dialect       string         `json:"session_dialect"`
	} `json:"sessions"`
	Tcons map[string]struct {
		Service     string `json:"service"`
		SessionID   string `json:"session_id"`
		Machine     string `json:"machine"`
		ConnectedAt string `json:"connected_at"`
	} `json:"tcons"`
	OpenFiles map[string]statusOpenFile `json:"open_files"`
}

// ParseStatus reads the output of smbstatus --json without knowing the paths of the shares: an open file belongs to every
// connection of its smbd process. See ParseStatusPaths.
func ParseStatus(data []byte) ([]Connection, error) { return ParseStatusPaths(data, nil) }

// ParseStatusPaths reads the output of smbstatus --json. The list is never nil, and is ordered by the time of connecting,
// then user and share.
//
// An open file is attached to a connection when it is held by the connection's smbd process (never matched on an empty
// process id) and its service_path is the path of the connection's share, paths being the share names with their
// paths. Where the open file has no service_path, or the share is not in paths, the process id alone tells. The uid of
// an open is the one the share runs as (force user), not the session user, so it is not used.
func ParseStatusPaths(data []byte, paths map[string]string) ([]Connection, error) {
	var st statusJSON
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse smbstatus: %w", err)
	}
	out := []Connection{}
	for _, tc := range st.Tcons {
		since, err := time.Parse(time.RFC3339Nano, tc.ConnectedAt)
		if err != nil {
			return nil, fmt.Errorf("parse smbstatus: connection to %q: %w", tc.Service, err)
		}
		c := Connection{Share: tc.Service, Machine: tc.Machine, Since: since}
		if ses, ok := st.Sessions[tc.SessionID]; ok {
			c.User, c.Protocol, c.Address = ses.Username, ses.Dialect, hostOf(ses.Hostname)
			if ses.RemoteMachine != "" {
				c.Machine = ses.RemoteMachine
			}
			c.Files = openFiles(st.OpenFiles, ses.ServerID.PID, paths[tc.Service])
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Connection) int {
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		if c := strings.Compare(a.User, b.User); c != 0 {
			return c
		}
		return strings.Compare(a.Share, b.Share)
	})
	return out, nil
}

// openFiles are the files that the smbd process pid holds open in the share whose path is sharePath, relative to the
// share, sorted. An open without a service_path, or an unknown share path, matches by the process alone; an empty pid
// matches nothing.
func openFiles(all map[string]statusOpenFile, pid, sharePath string) []string {
	if pid == "" {
		return nil
	}
	var files []string
	for _, f := range all {
		if f.ServicePath != "" && sharePath != "" && path.Clean(f.ServicePath) != path.Clean(sharePath) {
			continue
		}
		for _, o := range f.Opens {
			if o.ServerID.PID == pid && !slices.Contains(files, f.Filename) {
				files = append(files, f.Filename)
			}
		}
	}
	slices.Sort(files)
	return files
}

// hostOf is the address of a client from the "ipv4:127.0.0.1:49908" form of smbstatus: without the family and the
// port. A string without the family prefix is returned as it is (a port cannot be told from the last group of an IPv6
// address).
func hostOf(s string) string {
	fam, rest, ok := strings.Cut(s, ":")
	if !ok || (fam != "ipv4" && fam != "ipv6") {
		return s
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
