package main

import (
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/api"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// register adds the extension's routes for the real host.
func register(r fiber.Router) { api.Register(r, hostAPI()) }

// hostAPI is the API of the real host. Without admin mode the process runs as the user: it reads what it may and changes
// nothing; in admin mode, as root, it also plans the changes and makes the accounts. Its own commands run in the
// environment of the constraints, with the tools looked up afresh for each request. The launcher starts the runner of a
// job from /proc/self/exe: the binary this process runs from, also after an extension update renamed a new one into its
// place; the steps of a job that run a --samba-tool run it from there too (Self). smbd's root preexec and the periodic
// script of the recycle bins call the binary by its path with the links resolved (Binary; edelweiss installs it at one
// that stays the same), for /proc/self/exe would name smbd or the shell there.
func hostAPI() *api.API {
	return &api.API{
		Root:     "/",
		Runner:   run.Exec{Env: run.Env},
		Jobs:     jobs.DefaultStore(),
		Launcher: jobs.ExecLauncher{Binary: "/proc/self/exe"},
		Self:     "/proc/self/exe",
		Binary:   executablePath(),
	}
}

// executablePath is the path of the running binary with its links resolved, or "" when it cannot be found: Render then
// refuses, and no change of the config is planned.
func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return resolvedPath(exe)
}

// resolvedPath is the path with its links resolved, or "" when that cannot be done (the file is gone).
func resolvedPath(p string) string {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return resolved
}

// runnerConfig is the configuration of the detached runner (--run-job): the real host, root only. Its commands run in the
// environment of the constraints.
func runnerConfig() jobs.RunnerConfig {
	return jobs.RunnerConfig{
		Store:       jobs.DefaultStore(),
		Exec:        jobs.Executor{Root: "/", Runner: run.Exec{Env: run.Env}},
		RequireRoot: true,
	}
}
