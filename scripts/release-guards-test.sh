#!/bin/sh
# Tests the guards of the release targets (make test): that VERSION is given and its format in release-check, the files
# in dist/ that gh-release accepts, and that make release passes the version into the binaries and checks the version
# that a binary reports. It runs make in a temporary copy of the Makefile and the manifest, so it never touches the dist/
# of this working copy. gh is a stub that records its arguments, and go, npm, npx and node are stubs that fail:
# gh-release builds nothing, and make release is a dry run (make -n) or runs with fake binaries and a go stub that only
# knows the host (go env). It never contacts GitHub.
# VERBOSE=1 lists every check that passes; a failing check is always listed.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
FAILED=0
PASSED=0
pass() { PASSED=$((PASSED + 1)); [ -z "${VERBOSE:-}" ] || echo "ok   $*"; }
bad() { echo "FAIL $*" >&2; FAILED=$((FAILED + 1)); }

# The version of the release in these checks: any valid version works, because the version of a release comes from the
# tag, and the manifest holds the placeholder.
V=0.1.0
B=edelweiss-ext-samba
# make takes VERSION from the environment, and also from the MAKEFLAGS of a calling make (make test VERSION=0.2.0 hands
# the command-line variable on that way); the checks below give it explicitly or not at all.
unset VERSION MAKEFLAGS
# mkrepo <dir>: a copy of the Makefile and the manifest
mkrepo() {
  mkdir -p "$1"
  cp "$ROOT/Makefile" "$ROOT/manifest.json" "$1/"
}
R=$T/repo
mkrepo "$R"

mkdir "$T/bin"
cat >"$T/bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" >>"$GH_LOG"
EOF
for tool in go npm npx node; do
  cat >"$T/bin/$tool" <<EOF
#!/bin/sh
echo "$tool \$*" >>"$T/forbidden.log"
exit 1
EOF
done
chmod +x "$T"/bin/*
GH_LOG=$T/gh.log
PATH=$T/bin:$PATH
export GH_LOG PATH

mk() { make -s -C "$R" "$@"; }

echo "== release-check accepts a good version"
if mk release-check "VERSION=$V" >"$T/out" 2>&1; then pass "release-check VERSION=$V"; else bad "release-check VERSION=$V: $(cat "$T/out")"; fi

echo "== release-check needs a VERSION"
# version_required <what> [make arguments]: release-check has to stop with its own message, not with the format one.
version_required() {
  what=$1
  shift
  if mk release-check "$@" >"$T/out" 2>"$T/err"; then
    bad "release-check accepted $what"
  elif ! grep -q "VERSION is required" "$T/err"; then
    bad "release-check $what was not rejected for the missing VERSION: $(cat "$T/err")"
  elif grep -q "is not a version like" "$T/err"; then
    bad "release-check $what was rejected for its format: $(cat "$T/err")"
  else
    pass "release-check rejects $what"
  fi
}
version_required "a missing VERSION"
version_required "an empty VERSION" "VERSION="

echo "== release-check rejects a VERSION that is not a Go-compatible version"
NL='
'
format_rejected() {
  rm -f "$T/pwned"
  if mk release-check "VERSION=$1" >"$T/out" 2>"$T/err"; then
    bad "release-check accepted VERSION=[$1]"
  elif ! grep -q "is not a version like" "$T/err"; then
    bad "release-check VERSION=[$1] was not rejected for its format: $(cat "$T/err")"
  elif [ -e "$T/pwned" ]; then
    bad "release-check ran code from VERSION=[$1]"
  else
    pass "release-check rejects [$1]"
  fi
}
format_rejected "0.2.0+x"
format_rejected "$V+build.5"
format_rejected "v$V"
format_rejected "0.1"
format_rejected "1.2.3.4"
format_rejected "1.2.3-"
# Go's rules: no leading zeros in the numbers, no empty pre-release identifier, no leading zero in a numeric one.
format_rejected "01.2.3"
format_rejected "00.1.0"
format_rejected "1.02.3"
format_rejected "1.2.03"
format_rejected "1.2.3-rc..1"
format_rejected "1.2.3-."
format_rejected "1.2.3-.rc"
format_rejected "1.2.3-rc."
format_rejected "1.2.3-01"
format_rejected "1.2.3-rc.01"
format_rejected "$V "
format_rejected "$V${NL}x"
format_rejected "$V\"; touch $T/pwned; \""
format_rejected "$V\`touch $T/pwned\`"
# Other versions of a good format pass release-check: the tag is the only source of the version, and the manifest (the
# placeholder) does not matter.
format_accepted() {
  if ! mk release-check "VERSION=$1" >"$T/out" 2>"$T/err"; then
    bad "release-check rejected VERSION=$1: $(cat "$T/err")"
  elif ! grep -Fxq "release-check: $1 OK" "$T/out"; then
    bad "release-check VERSION=$1 did not report OK: $(cat "$T/out")"
  else
    pass "release-check accepts $1"
  fi
}
format_accepted "0.2.0-rc.1"
format_accepted "1.0.0-alpha.0"
format_accepted "10.20.30"
format_accepted "0.0.0"
format_accepted "1.2.3-0"
format_accepted "1.2.3-0A"
format_accepted "1.2.3-rc-1"
format_accepted "1.2.3--"
format_accepted "1.2.3-x.7.z.92"

FILES="${B}_${V}_linux_amd64 ${B}_${V}_linux_arm64"
good_dist() {
  rm -rf "$R/dist" "$GH_LOG"
  mkdir "$R/dist"
  for f in $FILES; do echo "content of $f" >"$R/dist/$f"; done
  (cd "$R/dist" && sha256sum $FILES >checksums.txt)
}
# refused <message> <what is wrong> [VERSION]: gh-release has to stop with that message (an extended regular expression
# for the output of make and of sha256sum), and without calling gh.
refused() {
  if mk gh-release "VERSION=${3:-$V}" >"$T/out" 2>"$T/err"; then
    bad "gh-release accepted: $2"
  elif [ -e "$GH_LOG" ]; then
    bad "gh-release called gh although it refused: $2"
  elif ! cat "$T/out" "$T/err" | grep -Eq "$1"; then
    bad "gh-release refused for another reason than [$1]: $2: $(cat "$T/out" "$T/err")"
  else
    pass "gh-release refuses: $2"
  fi
}
HOLD="dist/ has to hold exactly"
LIST="dist/checksums.txt has to list exactly"
MISSING="dist/checksums.txt is missing"

echo "== gh-release uploads exactly the release files"
good_dist
if mk gh-release "VERSION=$V" >"$T/out" 2>"$T/err"; then
  got=$(grep '^dist/' "$GH_LOG" | LC_ALL=C sort | tr '\n' ' ')
  want=$(for f in checksums.txt $FILES; do echo "dist/$f"; done | LC_ALL=C sort | tr '\n' ' ')
  [ "$got" = "$want" ] && pass "gh-release uploads $got" || bad "gh-release uploaded [$got], expected [$want]"
  [ "$(sed -n '1,4p' "$GH_LOG" | tr '\n' ' ')" = "release create v$V --verify-tag " ] ||
    bad "unexpected gh call: $(tr '\n' ' ' <"$GH_LOG")"
  ! grep -Fxq -- --prerelease "$GH_LOG" || bad "gh-release marked the release $V as a pre-release"
  [ ! -e "$T/forbidden.log" ] || bad "gh-release ran a build tool: $(cat "$T/forbidden.log")"
else
  bad "gh-release refused a good dist/: $(cat "$T/err")"
fi

echo "== gh-release refuses a dist/ that is not exactly what make release writes"
good_dist; echo x >"$R/dist/notes.txt"; refused "$HOLD" "an extra file"
good_dist; echo x >"$R/dist/checksumsXtxt"; refused "$HOLD" "checksumsXtxt (slipped through the regular expression of the old check)"
good_dist; echo x >"$R/dist/.hidden"; refused "$HOLD" "a hidden file"
good_dist; mkdir "$R/dist/sub"; refused "$HOLD" "a subdirectory"
good_dist; cp "$R/dist/${B}_${V}_linux_amd64" "$R/dist/${B}_0.0.9_linux_amd64"
refused "$HOLD" "a stale binary of another version"
good_dist; cp "$R/dist/${B}_${V}_linux_amd64" "$R/dist/${B}_0.0.9_linux_amd64"
(cd "$R/dist" && sha256sum "${B}_0.0.9_linux_amd64" >>checksums.txt)
refused "$HOLD" "a stale binary of another version that checksums.txt lists"
good_dist; rm "$R/dist/${B}_${V}_linux_arm64"; refused "could not be read|No such file" "a missing binary that checksums.txt lists"
good_dist; rm "$R/dist/${B}_${V}_linux_arm64"; grep -v arm64 "$R/dist/checksums.txt" >"$T/sums"; cp "$T/sums" "$R/dist/checksums.txt"
refused "$HOLD" "a missing binary that checksums.txt does not list"
good_dist; echo corrupt >>"$R/dist/${B}_${V}_linux_amd64"; refused "did NOT match" "a corrupted file"
good_dist; rm "$R/dist/checksums.txt"; refused "$MISSING" "no checksums.txt"
good_dist; grep arm64 "$R/dist/checksums.txt" >"$T/sums"; cat "$T/sums" >>"$R/dist/checksums.txt"
refused "$LIST" "a file listed twice in checksums.txt"
good_dist; sed '1s/^[0-9a-f]\{64\}/abc/' "$R/dist/checksums.txt" >"$T/sums"; cp "$T/sums" "$R/dist/checksums.txt"
refused "$LIST|did NOT match" "a checksums.txt line without a SHA-256 (GNU sha256sum -c skips it, busybox fails it)"
good_dist; rm -rf "$R/dist"; refused "$MISSING" "no dist/"
good_dist; refused "$HOLD" "a good dist/ of $V with another VERSION" 0.0.9
good_dist; rm -f "$R/dist"/*; for f in "${B}_0.0.9_linux_amd64" "${B}_0.0.9_linux_arm64"; do
  echo "content of $f" >"$R/dist/$f"
done
(cd "$R/dist" && sha256sum "${B}"_0.0.9_* >checksums.txt)
refused "$HOLD" "a complete dist/ of another version"

echo "== gh-release marks a version with a pre-release part (-) as a pre-release"
# The installer takes GitHub's latest release, which leaves pre-releases out: an rc must not be offered as an update.
RC=0.2.0-rc.1
RR=$T/repo-rc
mkrepo "$RR"
RCFILES="${B}_${RC}_linux_amd64 ${B}_${RC}_linux_arm64"
rm -f "$GH_LOG"
mkdir "$RR/dist"
for f in $RCFILES; do echo "content of $f" >"$RR/dist/$f"; done
(cd "$RR/dist" && sha256sum $RCFILES >checksums.txt)
if make -s -C "$RR" gh-release "VERSION=$RC" >"$T/out" 2>"$T/err"; then
  if grep -Fxq -- --prerelease "$GH_LOG" && grep -Fxq "v$RC" "$GH_LOG"; then
    pass "gh-release v$RC passes --prerelease"
  else
    bad "gh-release v$RC did not pass --prerelease: $(tr '\n' ' ' <"$GH_LOG")"
  fi
  got=$(grep '^dist/' "$GH_LOG" | LC_ALL=C sort | tr '\n' ' ')
  want=$(for f in checksums.txt $RCFILES; do echo "dist/$f"; done | LC_ALL=C sort | tr '\n' ' ')
  [ "$got" = "$want" ] && pass "gh-release v$RC uploads $got" || bad "gh-release v$RC uploaded [$got], expected [$want]"
else
  bad "gh-release refused a good dist/ of $RC: $(cat "$T/out" "$T/err")"
fi

echo "== gh-release builds nothing"
# The publish job has neither Go nor Node, so release-check has to be the only prerequisite of gh-release: a build
# prerequisite (build, ui, ...) would run there. make -p prints the rules, -n runs nothing.
got=$(mk -pn gh-release "VERSION=$V" 2>/dev/null | grep -E '^gh-release:')
if [ "$got" = "gh-release: release-check" ]; then
  pass "gh-release depends on release-check only"
else
  bad "the prerequisites of gh-release are [$got], expected [gh-release: release-check]"
fi
# Its recipe must not build either, and the dry run has to succeed (a missing prerequisite file stops it).
if ! mk -n gh-release "VERSION=$V" >"$T/dry" 2>&1; then
  bad "make -n gh-release failed: $(cat "$T/dry")"
elif grep -Eq '^(go|npm|npx|node|make|mkdir|for|find|cp|rm) ' "$T/dry"; then
  bad "gh-release would build: $(grep -E '^(go|npm|npx|node|make|mkdir|for|find|cp|rm) ' "$T/dry")"
else
  pass "make -n gh-release runs no build command"
fi
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

echo "== release passes the version into the binaries"
# make -n prints the recipes and runs nothing, so the stubbed tools are not called. The ldflags are checked as a whole,
# so a version that is cut off or followed by more text does not pass.
if ! mk -n release "VERSION=$V" >"$T/dry" 2>&1; then
  bad "make -n release failed: $(cat "$T/dry")"
elif ! grep -F 'go build' "$T/dry" | grep -Fq -- "-ldflags \"-s -w -X main.version=$V\""; then
  bad "the go build line of make release does not pass -X main.version=$V: $(grep -F 'go build' "$T/dry")"
else
  pass "make release VERSION=$V builds with -X main.version=$V"
fi
if ! grep -Fq -- '--edelweiss-manifest' "$T/dry"; then
  bad "make release does not run a binary to check its version: $(cat "$T/dry")"
else
  pass "make release checks the version of a binary"
fi
# Without a version (a plain make build) the binaries report the placeholder of the manifest.
if ! mk -n build >"$T/dry" 2>&1; then
  bad "make -n build failed: $(cat "$T/dry")"
elif ! grep -Fq 'go build' "$T/dry"; then
  bad "make -n build prints no go build line: $(cat "$T/dry")"
elif grep -Fq 'main.version' "$T/dry"; then
  bad "make build without VERSION sets main.version: $(grep -F 'main.version' "$T/dry")"
else
  pass "make build without VERSION does not set main.version"
fi
# A VERSION that reaches build (make build VERSION=..., make -k, make -j, an exported VERSION) has to pass release-check
# first, because a binary with an invalid version panics at start; without a VERSION, build must not need it (CI runs
# make build).
if ! mk -pn build "VERSION=$V" 2>/dev/null | grep -E '^build:' | grep -Fqw release-check; then
  bad "build does not depend on release-check when VERSION is set"
else
  pass "build depends on release-check when VERSION is set"
fi
if mk -pn build 2>/dev/null | grep -E '^build:' | grep -Fqw release-check; then
  bad "build depends on release-check although VERSION is not set"
else
  pass "build does not depend on release-check without VERSION"
fi
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

echo "== make release checks the version that the binary reports"
# make -o build uses the fake binaries in build/ (shell scripts that print a manifest line) instead of building them. The
# go stub answers go env GOHOSTOS and GOHOSTARCH with STUB_OS and STUB_ARCH and nothing else; it comes in through GO=
# only, so every other check keeps the go that fails. It all happens in a temporary copy of the repository.
RV=$T/repo-release
mkrepo "$RV"
mkdir "$RV/build"
cat >"$T/gostub" <<EOF
#!/bin/sh
[ "\$1 \$2" = "env GOHOSTOS" ] && { echo "\$STUB_OS"; exit 0; }
[ "\$1 \$2" = "env GOHOSTARCH" ] && { echo "\$STUB_ARCH"; exit 0; }
echo "gostub \$*" >>"$T/forbidden.log"
exit 1
EOF
chmod +x "$T/gostub"
# fake_binary <arch> <exit status> <output>: build/<binary>_linux_<arch> prints <output> (nothing if it is empty) and exits
# with the status.
fake_binary() {
  if [ -n "$3" ]; then
    body="cat <<'EOT'
$3
EOT"
  else
    body=:
  fi
  printf '#!/bin/sh\n%s\nexit %s\n' "$body" "$2" >"$RV/build/${B}_linux_$1"
  chmod +x "$RV/build/${B}_linux_$1"
}
# fake_binaries <exit status> <output>: the same for both architectures.
fake_binaries() {
  fake_binary amd64 "$1" "$2"
  fake_binary arm64 "$1" "$2"
}
# manifest_line <version> <sdkVersion>: what a binary prints for --edelweiss-manifest (compact, sdkVersion after version).
manifest_line() { printf '{"id":"samba","version":"%s","sdkVersion":"%s"}' "$1" "$2"; }
# release_run <host os> <host arch>: make release in the temporary repository, on a machine that is <os>/<arch> for the go stub.
release_run() {
  rm -rf "$RV/dist"
  env "STUB_OS=$1" "STUB_ARCH=$2" make -s -C "$RV" -o build release "VERSION=$V" "GO=$T/gostub" >"$T/out" 2>"$T/err"
}
# release_ok <what> <host os> <host arch>: make release has to succeed and write dist/checksums.txt of the two files.
release_ok() {
  if ! release_run "$2" "$3"; then
    bad "make release refused: $1: $(cat "$T/err")"
  elif ! (cd "$RV/dist" && sha256sum -c checksums.txt >/dev/null 2>&1) ||
    [ "$(ls "$RV/dist" | LC_ALL=C sort | tr '\n' ' ')" != "checksums.txt ${B}_${V}_linux_amd64 ${B}_${V}_linux_arm64 " ]; then
    bad "make release wrote no good dist/: $1: $(ls "$RV/dist")"
  else
    pass "make release accepts: $1"
  fi
}
# release_refused <what> <regexp> <host os> <host arch>: make release has to stop with a message that matches <regexp>,
# and must not write checksums.txt.
release_refused() {
  if release_run "$3" "$4"; then
    bad "make release accepted: $1"
  elif ! grep -Eq "$2" "$T/err"; then
    bad "make release refused for another reason than [$2]: $1: $(cat "$T/err")"
  elif [ -e "$RV/dist/checksums.txt" ]; then
    bad "make release wrote checksums.txt although it refused: $1"
  else
    pass "make release refuses: $1"
  fi
}
fake_binaries 0 "$(manifest_line "$V" 0.2.0)"
release_ok "binaries that report the version $V" linux amd64
# The sdkVersion is not the version of the extension (the check must not take it for it).
fake_binaries 0 "$(manifest_line 0.0.0-dev "$V")"
release_refused "binaries that report 0.0.0-dev (and $V as sdkVersion)" 'reports version "0\.0\.0-dev"' linux amd64
fake_binaries 0 "$(manifest_line 0.1.0-rc.1 0.2.0)"
release_refused "binaries that report another version" 'reports version "0\.1\.0-rc\.1"' linux amd64
fake_binaries 3 "$(manifest_line "$V" 0.2.0)"
release_refused "a binary that prints the right version and then fails" "edelweiss-manifest failed" linux amd64
fake_binaries 0 ""
release_refused "a binary that prints nothing" 'reports version ""' linux amd64
# The binary of the host architecture is the one that is run.
fake_binary amd64 0 "$(manifest_line 0.0.0-dev 0.2.0)"
fake_binary arm64 0 "$(manifest_line "$V" 0.2.0)"
release_refused "a wrong binary for the host architecture amd64" 'reports version "0\.0\.0-dev"' linux amd64
release_ok "a good binary for the host architecture arm64" linux arm64
fake_binaries 0 "$(manifest_line "$V" 0.2.0)"
release_refused "a host that is not linux" "cannot be run on this machine" darwin amd64
release_refused "a host architecture that release does not build" "cannot be run on this machine" linux riscv64
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

if [ "$FAILED" -ne 0 ]; then
  echo "release guards test: $FAILED failed" >&2
  exit 1
fi
echo "== release guards test OK ($PASSED checks)"
