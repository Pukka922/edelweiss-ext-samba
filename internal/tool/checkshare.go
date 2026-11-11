package tool

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// checkShare is the root preexec of a share: it ends with 0 only when the share exists and paths.Available says it is
// there, and, with --client, the client is in no network of a Docker or libvirt bridge that is there now (clientAllowed),
// so smbd refuses every other connection (it fails closed). It takes no lock: it only reads the config, the mounts, the
// folder and the interfaces, and the API and the job runner may hold their locks for as long as they like.
//
// The name is the %S of smbd. Share names are not case-sensitive for Samba, so an exact match comes first and then a
// single match without case; several of those refuse. The client is the %I of smbd; the root preexec of an smb.conf of an
// older version has none, and the client is not looked at then.
func (t Tool) checkShare(_ context.Context, args []string) error {
	fs := newFlags("check-share")
	name := fs.String("name", "", "the share")
	client := fs.String("client", "", "the address of the client")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return usagef("--name is required")
	}
	cfg, s, err := t.available(*name)
	if err != nil {
		return err
	}
	if !given(fs, "client") {
		return nil
	}
	return t.clientAllowed(s.Name, *client, cfg.Settings.Interfaces)
}

// given reports whether the flag is among the arguments, also with an empty value.
func given(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

// clientAllowed refuses a client in a network of a bridge of Docker or libvirt that is there now and not chosen
// (samba.BridgeSubnets of the interfaces read now): hosts allow excepts the bridges of the last render only, and Docker
// makes a new br-* network at a compose up. An address that cannot be read, and interfaces that cannot be read, refuse.
func (t Tool) clientAllowed(share, client string, chosen []string) error {
	addr, err := netip.ParseAddr(client)
	if err != nil {
		return fmt.Errorf("%s: the client address %q cannot be read", share, client)
	}
	addr = addr.WithZone("").Unmap()
	read := t.Ifaces
	if read == nil {
		read = samba.SystemInterfaces
	}
	ifs, err := read()
	if err != nil {
		return fmt.Errorf("%s: the network interfaces cannot be read: %w", share, err)
	}
	for _, p := range samba.BridgeSubnets(ifs, chosen) {
		if p.Contains(addr) {
			return fmt.Errorf("%s: the client %s is in %s, a network of a Docker or libvirt bridge", share, addr, p)
		}
	}
	return nil
}

// availableShare is the share of a subcommand: the config is read anew, the share is found as smbd names it (find), and
// its disk has to be available (paths.Available), else nothing is done and the reason is the error.
func (t Tool) availableShare(name string) (samba.Share, error) {
	_, s, err := t.available(name)
	return s, err
}

// available is availableShare with the config it read.
func (t Tool) available(name string) (samba.Config, samba.Share, error) {
	cfg, ok, err := samba.Load(t.FS)
	switch {
	case err != nil:
		return samba.Config{}, samba.Share{}, fmt.Errorf("%s: the config cannot be read: %w", name, err)
	case !ok:
		return samba.Config{}, samba.Share{}, fmt.Errorf("%s: the extension is not set up", name)
	}
	s, err := find(cfg.Shares, name)
	if err != nil {
		return samba.Config{}, samba.Share{}, err
	}
	if ok, reason := paths.Available(t.FS, s); !ok {
		return samba.Config{}, samba.Share{}, fmt.Errorf("%s: %s", s.Name, reason)
	}
	return cfg, s, nil
}

// find is the share that smbd means by name: the one of exactly that name, else the only one that differs in case.
func find(shares []samba.Share, name string) (samba.Share, error) {
	var folded []samba.Share
	for _, s := range shares {
		switch {
		case s.Name == name:
			return s, nil
		case strings.EqualFold(s.Name, name):
			folded = append(folded, s)
		}
	}
	switch len(folded) {
	case 0:
		return samba.Share{}, fmt.Errorf("%s: no such share", name)
	case 1:
		return folded[0], nil
	}
	return samba.Share{}, fmt.Errorf("%s: ambiguous, several shares differ only in case", name)
}
