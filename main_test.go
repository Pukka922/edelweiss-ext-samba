package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss/sdk"
	"github.com/Pukka922/edelweiss/sdk/sdktest"
)

// sdk.New panics on an invalid manifest, so building the extension proves the embedded manifest.json is valid. The
// committed manifest holds the placeholder version: a release gets its version from the git tag (manifestWithVersion).
func TestManifest(t *testing.T) {
	m := sdk.New(sdk.Options{Manifest: manifest}).Manifest
	if m.ID != "samba" {
		t.Fatalf("manifest id %q, want samba", m.ID)
	}
	if m.Version != "0.0.0-dev" {
		t.Fatalf("manifest version %q, want the placeholder 0.0.0-dev", m.Version)
	}
	if m.Menu.Section != "system" || m.Menu.Order != 50 {
		t.Fatalf("menu %+v, want section system, order 50", m.Menu)
	}
	if m.Name["de"] != "Freigaben" || m.Name["en"] != "Shares" {
		t.Fatalf("name %v, want Freigaben/Shares", m.Name)
	}
	if m.Description["de"] != "Samba-Freigaben wie bei einem NAS einrichten" || m.Description["en"] != "Set up Samba shares like on a NAS" {
		t.Fatalf("description %v", m.Description)
	}
	if m.RequiresAdmin {
		t.Fatal("requiresAdmin must be false: everybody may see the shares")
	}
	// The packages of internal/samba/testdata/README.md: Samba, mDNS for the Finder, and WS-Discovery for the Explorer.
	if want := []string{"samba", "avahi", "dbus", "wsdd"}; m.Requires == nil || !slices.Equal(m.Requires.APK, want) {
		t.Fatalf("requires %+v, want the apk packages %v", m.Requires, want)
	}
}

// Without a release version the manifest stays as it is.
func TestManifestWithoutVersion(t *testing.T) {
	got, err := manifestWithVersion(manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, manifest) {
		t.Fatalf("manifestWithVersion(manifest, \"\") = %q, want the manifest unchanged", got)
	}
}

// A release version replaces the version field and nothing else.
func TestManifestWithVersion(t *testing.T) {
	const v = "0.2.0-rc.1"
	raw, err := manifestWithVersion(manifest, v)
	if err != nil {
		t.Fatal(err)
	}
	got := sdk.New(sdk.Options{Manifest: raw}).Manifest
	want := sdk.New(sdk.Options{Manifest: manifest}).Manifest
	want.Version = v
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest = %+v, want the manifest.json one with version %s: %+v", got, v, want)
	}
	var gotFields, wantFields map[string]any
	if err := json.Unmarshal(raw, &gotFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifest, &wantFields); err != nil {
		t.Fatal(err)
	}
	wantFields["version"] = v
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("manifest fields = %v, want %v", gotFields, wantFields)
	}
}

func TestManifestWithVersionInvalidJSON(t *testing.T) {
	for _, raw := range []string{"{", "[]", "null"} {
		if got, err := manifestWithVersion([]byte(raw), "0.2.0"); err == nil {
			t.Fatalf("manifestWithVersion(%q) returned %q and no error", raw, got)
		}
	}
}

// The test binary acts as the extension's binary when asBinary is set: it runs main, which decides by its arguments what
// to be, as the binary started by the launcher does.
const asBinary = "EDELWEISS_SAMBA_MAIN_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(asBinary) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// --run-job <dir> makes the binary the runner of a job and nothing else: it ends with the status of RunJob (2: not a job
// directory) instead of serving the API. The directory here is none, so the test touches nothing of the host.
func TestRunJobEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "--run-job", filepath.Join(t.TempDir(), "20261020T120000-abcd"))
	cmd.Env = append(os.Environ(), asBinary+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(stderr.String(), "run-job: ") ||
		!strings.Contains(stderr.String(), "is not a job directory") {
		t.Fatalf("--run-job: %v, stderr %q; want exit status 2 of the runner, saying it is no job directory", err, stderr.String())
	}
}

// The runner works on the real host, as root only, with the commands in the environment of the constraints.
func TestRunnerConfig(t *testing.T) {
	want := jobs.RunnerConfig{
		Store:       jobs.DefaultStore(),
		Exec:        jobs.Executor{Root: "/", Runner: run.Exec{Env: run.Env}},
		RequireRoot: true,
	}
	if got := runnerConfig(); !reflect.DeepEqual(got, want) {
		t.Fatalf("runnerConfig() = %+v; want %+v", got, want)
	}
}

// asBinaryRun runs the test binary as the extension's binary with the arguments, and returns its exit status and stderr.
func asBinaryRun(t *testing.T, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), asBinary+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), stderr.String()
	}
	t.Fatalf("%v: %v", args, err)
	return -1, ""
}

// The first argument decides what the binary is: --samba-tool is the tool that tool.Main runs with what follows it, which
// ends with its status instead of serving the API. Only arguments that the tool refuses before it looks at the host are
// given here.
func TestMainDispatch(t *testing.T) {
	for _, c := range []struct {
		args   []string
		stderr string
	}{
		{[]string{"--samba-tool"}, "samba-tool: no subcommand"},
		{[]string{"--samba-tool", "bogus"}, `samba-tool: unknown subcommand "bogus"`},
	} {
		if code, stderr := asBinaryRun(t, c.args...); code != 2 || !strings.Contains(stderr, c.stderr) {
			t.Errorf("%v: status %d, stderr %q; want 2 and %q", c.args, code, stderr, c.stderr)
		}
	}
}

// The API reads the host the runner changes: the same root and jobs. Its own commands have the environment of the
// constraints, and the launcher starts the binary that runs. Every seam that a test replaces is the real one.
func TestHostAPI(t *testing.T) {
	a, cfg := hostAPI(), runnerConfig()
	if a.Root != "/" || a.Root != cfg.Exec.Root || a.Jobs != cfg.Store || a.Jobs != jobs.DefaultStore() {
		t.Errorf("API root %q, jobs %+v; the runner has %q, %+v", a.Root, a.Jobs, cfg.Exec.Root, cfg.Store)
	}
	if !reflect.DeepEqual(a.Runner, run.Exec{Env: run.Env}) {
		t.Errorf("runner %+v", a.Runner)
	}
	if want := (jobs.ExecLauncher{Binary: "/proc/self/exe"}); !reflect.DeepEqual(a.Launcher, want) {
		t.Errorf("launcher %+v; want %+v", a.Launcher, want)
	}
	if a.Self != "/proc/self/exe" {
		t.Errorf("Self %q; want /proc/self/exe", a.Self)
	}
	if a.Tools != nil || a.Accounts != nil || a.Ifaces != nil || a.Statfs != nil || a.Probe != nil || a.Now != nil || a.Poll != 0 {
		t.Errorf("the API of the host replaces something that is real: %+v", a)
	}
}

// smbd's root preexec and the periodic script call the binary by the path of the running binary with its links resolved:
// /proc/self/exe would name smbd or the shell there.
func TestHostAPIBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	if got := hostAPI().Binary; got != want || !filepath.IsAbs(got) {
		t.Errorf("Binary %q; want the absolute path of the running binary, %q", got, want)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolvedPath(filepath.Join(dir, "link")); got != filepath.Join(resolved, "real") {
		t.Errorf("resolvedPath of a link = %q", got)
	}
	if got := resolvedPath(filepath.Join(dir, "gone")); got != "" {
		t.Errorf("resolvedPath of a path that is not there = %q, want none", got)
	}
}

// register mounts the API. Nothing of the host is read: a change without admin mode is refused before it looks at
// anything, and an id that is no job id is no job.
func TestRegister(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: sdk.ErrorHandler})
	register(app)
	routes := map[string]bool{}
	for _, r := range app.GetRoutes() {
		routes[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{"GET /view", "GET /folders", "GET /browse", "POST /browse/mkdir", "GET /recycle",
		"GET /connections", "POST /jobs", "GET /jobs/:id", "POST /jobs/:id/cancel", "POST /users/samba", "POST /users/host",
		"POST /users/:name/password", "DELETE /users/:name", "POST /service-accounts"} {
		if !routes[want] {
			t.Errorf("no route %s; routes %v", want, routes)
		}
	}
	user := sdk.User{Name: "myUser", UID: 1000, Lang: "en"}
	for _, r := range []struct{ method, path string }{{"POST", "/jobs"}, {"POST", "/users/samba"}, {"DELETE", "/users/myuser"}} {
		resp := sdktest.Do(t, app, sdktest.Request(r.method, r.path, strings.NewReader("{}"), user))
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s without admin mode: status %d; want 403", r.method, r.path, resp.StatusCode)
		}
	}
	if resp := sdktest.Do(t, app, sdktest.Request("GET", "/jobs/x", nil, user)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /jobs/x: status %d; want 404", resp.StatusCode)
	}
}
