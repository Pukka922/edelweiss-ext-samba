package samba

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// ConfPath is the smb.conf that Render makes.
const ConfPath = "/etc/samba/smb.conf"

// includePrefix is the start of the include files of a share: edelweiss-r-<16 hex of the sha256 of the lower-case share
// name>-. The name of the share itself is not in the file name, so no share name and user name can make two files collide
// and no character of a name reaches a path.
func includePrefix(share string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(share)))
	return "/etc/samba/edelweiss-r-" + hex.EncodeToString(sum[:8]) + "-"
}

// IncludePath is the per-user include file of a share with a recycle bin: the file of a reader. smbd expands %U in lower
// case, so the user name is written in lower case.
func IncludePath(share, user string) string {
	return includePrefix(share) + strings.ToLower(user) + ".conf"
}

// hostsAllow is what hosts allow admits: loopback and the private ranges. hosts allow alone limits the clients
// (interfaces.txt, hosts-allow-loopback), so there is no hosts deny.
const hostsAllow = "127.0.0.1 ::1 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7 fe80::/10"

// hostsAllowLine is the value of hosts allow: hostsAllow EXCEPT the networks of the bridges of Docker and libvirt that are
// not chosen (BridgeSubnets), whose containers and VMs would reach the address of a chosen interface too. A bridge made
// after this render is refused at the connect by the root preexec (check-share --client).
func hostsAllowLine(c Config, env RenderEnv) string {
	line := hostsAllow
	if except := BridgeSubnets(env.Ifaces, c.Settings.Interfaces); len(except) > 0 {
		line += " EXCEPT"
		for _, p := range except {
			line += " " + p.String()
		}
	}
	return line
}

// Owner is the Unix user and group of a shared folder.
type Owner struct{ User, Group string }

// RenderEnv is what Render needs besides the config.
type RenderEnv struct {
	Owners     map[string]Owner // by share name: the owner of the folder
	SambaUsers map[string]bool  // the names that have a Samba entry; any other name is left out
	Binary     string           // the extension's executable, for the root preexec of each share: an absolute clean path
	NoXattr    map[string]bool  // the shares on a file system without extended attributes
	Ifaces     []Iface          // the network interfaces of the host (SystemInterfaces): the networks of its bridges
	// MDNS: smbd announces itself through avahi, as the discovery is on, on a local interface; without it multicast dns
	// register = no, so that an avahi-daemon that runs for something else does not announce smbd.
	MDNS bool
}

// Skip reasons of Result.Skipped.
const (
	SkipBadName   = "bad_name"   // ValidShareName refuses the name
	SkipCaseClash = "case_clash" // another share has the same name without case, or two readers differ only by case
	SkipNoOwner   = "no_owner"   // no owner, or no safe owner user or group name
	SkipRootOwned = "root_owned" // the owner is root
	SkipBadPath   = "bad_path"   // the path cannot be written to smb.conf as it is
	SkipNoUsers   = "no_users"   // no user with a right and a Samba entry: an empty valid users would admit every account
)

// Result is what Render made.
type Result struct {
	// Files are the files the extension owns: absolute path to content.
	Files map[string]string
	// Skipped says why a share is not in the config: share name to one of the Skip reasons. Two readers whose names
	// differ only by case get no include file (smbd looks it up in lower case); each is listed as "<share>/<lower-case
	// name>" with SkipCaseClash, and the share stays.
	Skipped map[string]string
}

var (
	userRE  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)
	ifaceRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)
	groupRE = userRE
	wgRE    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,15}$`)
)

// checkBinary reports why b cannot go into a root preexec: it has to be an absolute clean path without a quote, a %, or a
// control character.
func checkBinary(b string) error {
	if b == "" || !strings.HasPrefix(b, "/") || path.Clean(b) != b || !utf8.ValidString(b) || strings.ContainsAny(b, "'%") ||
		strings.ContainsFunc(b, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("the binary %q is not an absolute clean path without quote, %% or control character", b)
	}
	return nil
}

// Render makes the files the extension owns: ConfPath, and for each reader of a share with a recycle bin the include
// file IncludePath(share, reader), which hides #recycle from that reader. The result is the same for the same input. It
// fails, with nothing rendered, when env.Binary is not usable: without the root preexec an unplugged disk would be written
// to.
//
// A share that would break or open up the config is left out, with the reason in Result.Skipped (SkipBadName, SkipCaseClash,
// SkipNoOwner, SkipRootOwned, SkipBadPath, SkipNoUsers). Names without a Samba entry in the rights are not rendered.
func Render(c Config, env RenderEnv) (Result, error) {
	if err := checkBinary(env.Binary); err != nil {
		return Result{}, errors.New("render: " + err.Error())
	}
	res := Result{Files: map[string]string{}, Skipped: map[string]string{}}
	var b strings.Builder
	b.WriteString("# Made by edelweiss-ext-samba. Changes are overwritten.\n\n")
	writeGlobal(&b, c.Settings, hostsAllowLine(c, env), env.MDNS)

	shares := slices.Clone(c.Shares)
	slices.SortStableFunc(shares, func(x, y Share) int { return strings.Compare(x.Name, y.Name) })
	seen := map[string]bool{}
	for _, s := range shares {
		if ValidShareName(s.Name) == nil && seen[strings.ToLower(s.Name)] {
			res.Skipped[s.Name] = SkipCaseClash
			continue
		}
		if reason := writeShare(&b, res, s, env); reason != "" {
			res.Skipped[s.Name] = reason
		} else {
			seen[strings.ToLower(s.Name)] = true
		}
	}
	res.Files[ConfPath] = b.String()
	return res, nil
}

func writeGlobal(b *strings.Builder, s Settings, allow string, mdns bool) {
	min := "SMB3"
	if s.SMB2 {
		min = "SMB2_02"
	}
	wg := "WORKGROUP"
	if wgRE.MatchString(s.Workgroup) {
		wg = s.Workgroup
	}
	b.WriteString("[global]\n")
	line := func(f string, a ...any) { fmt.Fprintf(b, "\t"+f+"\n", a...) }
	line("server role = standalone server")
	line("workgroup = %s", wg)
	if wgRE.MatchString(s.Name) {
		line("netbios name = %s", s.Name)
	}
	line("disable netbios = yes")
	line("server min protocol = %s", min)
	line("smb ports = 445")
	if !mdns {
		line("multicast dns register = no")
	}
	line("interfaces = %s", interfaceList(s.Interfaces))
	line("bind interfaces only = yes")
	line("hosts allow = %s", allow)
	line("access based share enum = yes")
	line("map to guest = never")
	// the archive attribute lives in user.DOSATTRIB (store dos attributes), not in the execute bit of the owner: new files of
	// a share that runs as its owner are 0644, not 0744
	line("map archive = no")
	line("load printers = no")
	line("printing = bsd")
	line("printcap name = /dev/null")
	line("disable spoolss = yes")
	line("log file = /var/log/samba/log.%%m")
	line("max log size = 1000")
	line("fruit:aapl = yes")
	line("fruit:model = MacSamba")
	line("fruit:metadata = stream")
	line("fruit:nfs_aces = no")
	line("fruit:posix_rename = yes")
	line("fruit:veto_appledouble = no")
	line("fruit:delete_empty_adfiles = yes")
	line("fruit:wipe_intentionally_left_blank_rfork = yes")
}

// interfaceList is the interfaces line: the chosen names and always lo; sorted, each once. An empty choice binds lo only.
// A name that is not a plain interface name is left out: an interfaces entry may also be an address, a network or a
// wildcard.
func interfaceList(selected []string) string {
	names := []string{"lo"}
	for _, n := range selected {
		if ifaceRE.MatchString(n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return strings.Join(slices.Compact(names), " ")
}

// users are the names of rights that hold one of the rights, sorted; names without a Samba entry and unsafe names are
// not there.
func users(rights map[string]Right, env RenderEnv, want ...Right) []string {
	var out []string
	for u, r := range rights {
		if env.SambaUsers[u] && userRE.MatchString(u) && slices.Contains(want, r) {
			out = append(out, u)
		}
	}
	slices.Sort(out)
	return out
}

// value makes text one line of a smb.conf value: control characters become spaces, a % (a macro) goes, a final backslash
// (a line continuation) goes.
func value(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '%':
			return -1
		case r < 0x20 || r == 0x7f:
			return ' '
		}
		return r
	}, s)
	return strings.TrimRight(strings.TrimSpace(s), `\ `)
}

// safePath reports whether p can be written as the path of a share unchanged: absolute and clean, one line, no % and no
// final backslash, and no component that starts or ends with a space or holds two spaces in a row (smbd's parser folds
// them).
func safePath(p string) bool {
	if !strings.HasPrefix(p, "/") || !utf8.ValidString(p) || path.Clean(p) != p || value(p) != p || strings.HasSuffix(p, `\`) {
		return false
	}
	for _, comp := range strings.Split(p, "/") {
		if comp != strings.TrimSpace(comp) || strings.Contains(comp, "  ") {
			return false
		}
	}
	return true
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// writeShare writes the section of s and the include files of its readers. It returns "" when it wrote the share and the
// reason when it left it out.
func writeShare(b *strings.Builder, res Result, s Share, env RenderEnv) string {
	if ValidShareName(s.Name) != nil {
		return SkipBadName
	}
	owner := env.Owners[s.Name]
	if !userRE.MatchString(owner.User) || !groupRE.MatchString(owner.Group) {
		return SkipNoOwner
	}
	if owner.User == "root" {
		return SkipRootOwned
	}
	if !safePath(s.Path) {
		return SkipBadPath
	}
	valid := users(s.Rights, env, Read, Write)
	if len(valid) == 0 {
		return SkipNoUsers
	}
	xattr := !env.NoXattr[s.Name]

	fmt.Fprintf(b, "\n[%s]\n", s.Name)
	line := func(f string, a ...any) { fmt.Fprintf(b, "\t"+f+"\n", a...) }
	line("path = %s", s.Path)
	if cm := strings.Join(strings.Fields(value(s.Comment)), " "); cm != "" {
		line("comment = %s", cm)
	}
	if s.Hidden {
		line("browseable = no")
	} else {
		line("browseable = yes")
	}
	if s.PerUser {
		line("force group = %s", owner.Group)
		line("inherit owner = unix only")
		// The masks have no effect with acl_xattr:ignore system acls = yes: at the connect acl_xattr sets create mask
		// 0666, directory mask 0777 and inherit acls, and smbd then makes files 0666 and folders 0777 and leaves the rest
		// to a POSIX default ACL of the folder. That default ACL (user::rwx, group::rwx, other::r-x), which the conversion
		// writes on every folder (acl.Convert), is what keeps them from being world-writable: 0664 and 0775
		// (testdata/README.md, "Per-user shares"). The masks hold where acl_xattr is left out (NoXattr).
		line("create mask = 0664")
		line("directory mask = 2775")
		if xattr {
			line("acl_xattr:ignore system acls = yes")
			line("hide unreadable = yes")
		}
	} else {
		line("force user = %s", owner.User)
		line("force group = %s", owner.Group)
	}
	line("valid users = %s", strings.Join(valid, " "))
	if r := users(s.Rights, env, Read); len(r) > 0 {
		line("read list = %s", strings.Join(r, " "))
	}
	if w := users(s.Rights, env, Write); len(w) > 0 {
		line("write list = %s", strings.Join(w, " "))
	}
	line("root preexec = %s --samba-tool check-share --name '%%S' --client '%%I'", quote(env.Binary))
	line("root preexec close = yes")
	// acl_xattr only where the folder rights are: on a plain share an ACL edit from a client would write security.NTACL.
	vfs := []string{"catia"}
	if xattr {
		vfs = append(vfs, "fruit", "streams_xattr")
		if s.PerUser {
			vfs = append(vfs, "acl_xattr")
		}
	}
	if s.Recycle {
		vfs = append(vfs, "recycle")
	}
	line("vfs objects = %s", strings.Join(vfs, " "))
	if s.Recycle {
		line("recycle:touch = yes")
		line("recycle:versions = yes")
		line("recycle:keeptree = yes")
		line("recycle:repository = #recycle")
		if s.PerUser {
			// vfs_recycle makes #recycle and its folders as the user who deletes, 0700 by default: another user could
			// not move a file there, and vfs_recycle would remove it for good. The group of the share may write them.
			line("recycle:directory_mode = 0770")
			line("recycle:subdir_mode = 0770")
		}
		line("include = %s%%U.conf", includePrefix(s.Name))
		byLower := map[string][]string{}
		for _, u := range users(s.Rights, env, Read) {
			byLower[strings.ToLower(u)] = append(byLower[strings.ToLower(u)], u)
		}
		for lower, us := range byLower {
			if len(us) > 1 {
				res.Skipped[s.Name+"/"+lower] = SkipCaseClash
				continue
			}
			res.Files[IncludePath(s.Name, us[0])] = "veto files = /#recycle/\n"
		}
	}
	return ""
}
