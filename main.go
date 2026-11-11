// Command edelweiss-ext-samba is the Shares extension of edelweiss: Samba shares like on a NAS, in one static binary.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/tool"
	"github.com/Pukka922/edelweiss/sdk"
)

//go:embed manifest.json
var manifest []byte

// The version of a release: only the release build sets it, with -ldflags "-X main.version=<version>" (make release),
// and it is empty in every other build, which then reports the placeholder version of manifest.json.
var version string

// manifestWithVersion returns the manifest with its version field set to v. An empty v returns raw itself, so a build
// without a release version reports the placeholder of manifest.json.
func manifestWithVersion(raw []byte, v string) ([]byte, error) {
	if v == "" {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if fields == nil { // the JSON value null unmarshals without an error
		return nil, fmt.Errorf("manifest: not a JSON object")
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("manifest: encode version %q: %w", v, err)
	}
	fields["version"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return out, nil
}

// The built UI (make build runs npm run build in ui/ first). ui/dist holds a committed .gitkeep, so the package
// compiles before the first UI build too.
//
//go:embed all:ui/dist
var uiFiles embed.FS

func main() {
	// the detached runner of a job (see internal/jobs): no HTTP, no SDK; fd 3 is the job lock it was started with
	if len(os.Args) == 3 && os.Args[1] == "--run-job" {
		os.Exit(jobs.RunJob(runnerConfig(), os.Args[2], os.NewFile(3, "lock")))
	}
	// the tools that smbd and the steps of a job run (see internal/tool): no HTTP, no SDK either; what follows is the subcommand
	if len(os.Args) >= 2 && os.Args[1] == "--samba-tool" {
		os.Exit(tool.Main(os.Args[2:]))
	}
	// the daily cleanup of the recycle bins, started as root by the periodic script (see internal/recycle)
	if len(os.Args) == 2 && os.Args[1] == "--run-recycle-cleanup" {
		os.Exit(tool.RunRecycleCleanup())
	}
	m, err := manifestWithVersion(manifest, version)
	if err != nil {
		panic(err)
	}
	ui, err := fs.Sub(uiFiles, "ui/dist")
	if err != nil {
		panic(err)
	}
	ext := sdk.New(sdk.Options{Manifest: m, UI: ui})
	register(ext.App)
	ext.Run()
}
