package samba

import (
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// ReloadCmd has the running smbd read smb.conf again: smbcontrol smbd reload-config. It fails when no smbd runs
// ("all" would exit 0 then), so a caller knows that nothing was reloaded.
func ReloadCmd(t host.Tools) run.Cmd {
	return run.Cmd{Path: t.Smbcontrol, Args: []string{"smbd", "reload-config"}}
}
