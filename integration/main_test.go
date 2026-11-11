//go:build integration

// Package integration runs the commands of the extension against the real Samba, OpenRC and busybox of Alpine: first
// as captured outputs (the testdata of internal/samba), later through the packages of the extension. The commands run
// through run.Exec, the runner of the extension, so the tools get exactly the arguments, the environment and the working
// directory the extension gives them. scripts/test-alpine.sh runs each test in a throwaway Alpine container of its own,
// which installs the packages and starts the system log with OpenRC; the tests run as root there
// (EDELWEISS_ALPINE_TEST=1). Anywhere else the tests refuse to run.
package integration

import (
	"fmt"
	"os"
	"testing"
)

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestMain(m *testing.M) {
	if !exists("/.dockerenv") || !exists("/etc/alpine-release") || os.Getenv("EDELWEISS_ALPINE_TEST") != "1" ||
		os.Geteuid() != 0 {
		fmt.Println("integration: skipped – these tests install and start Samba; scripts/test-alpine.sh runs them in throwaway Alpine containers")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
