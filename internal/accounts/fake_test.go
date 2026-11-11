package accounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/etcfile"
)

var bg = context.Background()

// host is a root with the account files of an Alpine host and a busybox fake that changes them as the applets do.
type host struct {
	t    *testing.T
	root string
	bb   *busybox.Fake
	s    *Service
	// hash is what a new user gets in /etc/shadow ("!" as busybox adduser -D writes it).
	hash string
}

const (
	hostPasswd = "root:x:0:0:root:/root:/bin/ash\nmyUser:x:1000:1000::/home/myUser:/bin/sh\n"
	hostShadow = "root:*::0:::::\nmyUser:!:20761:0:99999:7:::\n"
	hostGroup  = "root:x:0:root\nusers:x:100:\nvideo:x:101:\nwheel:x:102:root\nmyUser:x:1000:\n"
)

func newHost(t *testing.T) *host {
	t.Helper()
	h := &host{t: t, root: t.TempDir(), bb: &busybox.Fake{}, hash: "!"}
	writeFile(t, h.root, PasswdFile, hostPasswd, 0o644)
	writeFile(t, h.root, ShadowFile, hostShadow, 0o640)
	writeFile(t, h.root, GroupFile, hostGroup, 0o644)
	h.bb.OnRun = h.applet
	h.s = &Service{Root: h.root, Busybox: h.bb,
		Writer: etcfile.Writer{Root: h.root, Tries: 3, Wait: time.Millisecond, LockTimeout: 50 * time.Millisecond},
		Lock:   Lock{Path: filepath.Join(h.root, "run/edelweiss-ext-users.lock"), Wait: 100 * time.Millisecond}}
	return h
}

func (h *host) read(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// line is the entry called name of rel, "" when there is none.
func (h *host) line(rel, name string) string {
	h.t.Helper()
	for _, l := range strings.Split(h.read(rel), "\n") {
		if strings.HasPrefix(l, name+":") {
			return l
		}
	}
	return ""
}

func (h *host) db() *DB {
	h.t.Helper()
	db, err := Load(h.root, now)
	if err != nil {
		h.t.Fatal(err)
	}
	return db
}

func (h *host) write(rel, data string) { writeFile(h.t, h.root, rel, data, 0o644) }

func (h *host) wantCommands(want ...string) {
	h.t.Helper()
	if got := h.bb.Commands(); !slices.Equal(got, want) {
		h.t.Fatalf("commands:\n got %q\nwant %q", got, want)
	}
}

func wantCode(t *testing.T, err error, code, field string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v (%T), want an *Error with code %s", err, err, code)
	}
	if e.Code != code || e.Field != field {
		t.Fatalf("error code %q field %q (%s), want %q field %q", e.Code, e.Field, e.Message, code, field)
	}
}

// applet is what busybox does to the files, in the lines captured on Alpine (testdata/users.txt in internal/samba).
func (h *host) applet(args []string) error {
	var flags []string
	var rest []string
	opts := map[string]string{}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-s" || a == "-u" || a == "-G" || a == "-g":
			i++
			opts[a] = args[i]
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		default:
			rest = append(rest, a)
		}
	}
	system := slices.Contains(flags, "-S")
	groups := ParseGroup(h.read(GroupFile))
	nextID := func(system bool, used func(int) bool) int {
		id := 1000
		if system {
			id = 100
		}
		for used(id) {
			id++
		}
		return id
	}
	switch args[0] {
	case "addgroup":
		if len(rest) == 2 { // addgroup <user> <group>
			return h.member(rest[1], rest[0])
		}
		gid, _ := strconv.Atoi(opts["-g"])
		if gid == 0 {
			gid = nextID(system, func(id int) bool {
				return slices.ContainsFunc(groups, func(g GroupEntry) bool { return g.GID == id })
			})
		}
		h.write(GroupFile, h.read(GroupFile)+fmt.Sprintf("%s:x:%d:\n", rest[0], gid))
	case "adduser":
		name := rest[0]
		var gid int
		for _, g := range groups {
			if g.Name == opts["-G"] {
				gid = g.GID
			}
		}
		pw := ParsePasswd(h.read(PasswdFile))
		uid, _ := strconv.Atoi(opts["-u"])
		if uid == 0 {
			uid = nextID(system, func(id int) bool {
				return slices.ContainsFunc(pw, func(e PasswdEntry) bool { return e.UID == id })
			})
		}
		h.write(PasswdFile, h.read(PasswdFile)+fmt.Sprintf("%s:x:%d:%d::/home/%s:%s\n", name, uid, gid, name, opts["-s"]))
		h.write(ShadowFile, h.read(ShadowFile)+fmt.Sprintf("%s:%s:20761:0:99999:7:::\n", name, h.hash))
		return h.member(opts["-G"], name)
	case "delgroup":
		h.write(GroupFile, dropEntry(h.read(GroupFile), rest[0]))
	default:
		return errors.New("unexpected applet " + args[0])
	}
	return nil
}

func (h *host) member(group, user string) error {
	for _, g := range ParseGroup(h.read(GroupFile)) {
		if g.Name == group {
			h.write(GroupFile, setMembers(h.read(GroupFile), group, append(g.Members, user)))
			return nil
		}
	}
	return errors.New("no group " + group)
}

func dropEntry(data, name string) string {
	var keep []string
	for _, l := range strings.SplitAfter(data, "\n") {
		if !strings.HasPrefix(l, name+":") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "")
}

func setMembers(data, group string, members []string) string {
	out, _ := etcfile.SetField([]byte(data), group, 3, strings.Join(members, ","))
	return string(out)
}

// addMember lists user in the members of group, as a host administrator with addgroup would.
func (h *host) addMember(group, user string) {
	h.t.Helper()
	if err := h.member(group, user); err != nil {
		h.t.Fatal(err)
	}
}
