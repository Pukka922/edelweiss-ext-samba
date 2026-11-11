package api

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// ConnectionView is a client connected to a share (samba.Connection), with the files it holds open in it.
type ConnectionView struct {
	User     string    `json:"user"`
	Machine  string    `json:"machine"`
	Address  string    `json:"address"`
	Protocol string    `json:"protocol"`
	Share    string    `json:"share"`
	Since    time.Time `json:"since"`
	Files    []string  `json:"files"`
}

// connections answers GET /connections (admin mode; smbstatus needs root): smbstatus --json, whose open files are
// attributed to their share by the paths of the shares of the config (samba.ParseStatusPaths).
func (a *API) connections(c fiber.Ctx) error {
	cfg, err := a.setUpConfig()
	if err != nil {
		return err
	}
	t := a.tools()
	if t.Smbstatus == "" {
		return failed("smbstatus is not installed")
	}
	out, err := a.Runner.Run(ctxOf(c), run.Cmd{Path: t.Smbstatus, Args: []string{"--json"}})
	if err != nil {
		return failed("smbstatus: %v", err)
	}
	sharePaths := map[string]string{}
	for _, s := range cfg.Shares {
		sharePaths[s.Name] = s.Path
	}
	conns, err := samba.ParseStatusPaths([]byte(out), sharePaths)
	if err != nil {
		return failed("%v", err)
	}
	resp := make([]ConnectionView, 0, len(conns))
	for _, cn := range conns {
		resp = append(resp, ConnectionView{User: cn.User, Machine: cn.Machine, Address: cn.Address, Protocol: cn.Protocol,
			Share: cn.Share, Since: cn.Since, Files: orEmpty(cn.Files)})
	}
	return c.JSON(resp)
}
