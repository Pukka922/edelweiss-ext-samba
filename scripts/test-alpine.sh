#!/bin/sh
# make test-alpine: runs the tests against the real Samba of Alpine (integration/, go test -tags integration) inside
# throwaway Alpine containers, never on this machine. The test binary and the extension's binary are built here (static,
# CGO_ENABLED=0) and reach the container read-only at /t; the end-to-end tests run the extension's API in the test
# process, which starts the runner of a job from /t/edelweiss-ext-samba, as smbd's root preexec and the periodic script
# call it. Each scenario runs in a container of its own (docker run --rm --init alpine:<tag>), which is removed afterwards.
# A scenario gets no extra privileges, or the capabilities its line in the table below names: NET_ADMIN, which only the
# scenarios of NET_ADMIN_TESTS may have (a second address or interface), or SYS_ADMIN, which the kernel needs for writing
# the security.NTACL attribute of the folder rights and which only the folder-rights scenarios (a name with "ACL" in it)
# may have. Never --privileged, never a device.
# Each container also gets a data disk at /srv/data: an anonymous volume of Docker (-v /srv/data), a real mount of the file
# system of Docker's data root there (ext4, which keeps the attributes of the folder rights), which --rm removes with the
# container, whatever the test left in it: nothing of it stays on this machine, also when a scenario is killed. run.sh gives
# it to root (0755, as a disk that was just formatted).
# The container first runs setup.sh, which this script writes: it installs, from Alpine's mirrors (the container's only
# network access), what the extension requires (samba, avahi, dbus, and wsdd for WS-Discovery) plus what the tests use
# (OpenRC and busybox-openrc for the init scripts, samba-client for smbclient and smbcacls, openssh for sshd), creates
# /run/openrc/softlevel (an empty one is enough for rc-service; there is no booted OpenRC) and starts the system log as
# OpenRC's service syslog, as on a host (crond needs that service; a syslogd started by hand would leave
# rc-service crond start failing). It starts no Samba: the tests do that. Then the test runs as root (run.sh).
# --init is needed: a daemon that is killed would stay a zombie without a process that reaps it.
# A scenario passes only when docker exits 0 and its output has the "--- PASS: <name>" line, so a renamed test cannot
# pass silently. The script fails when no scenario ran.
# Usage: scripts/test-alpine.sh [<scenario>...] runs the named scenarios (all without a name) on every image;
# TESTS=<text> runs only the scenarios whose name contains it, TEST_ALPINE_IMAGES="<image>..." uses these images instead of
# alpine:3.24 and alpine:3.22. TEST_ALPINE_LOG=<file> writes the whole output to that file as well.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
OUT=$(mktemp -d)
trap 'rm -rf "$WORK" "$OUT"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
GO=${GO:-go}
IMAGES=${TEST_ALPINE_IMAGES:-alpine:3.24 alpine:3.22}
# The scenarios, one per line: the test, the script that runs it in the container, the capabilities it gets ("-" for none,
# else a comma-separated list for --cap-add), then, when it is not setup.sh, the script that sets the container up.
SCENARIOS='TestCapture run.sh -
TestCaptureACL run.sh SYS_ADMIN
TestACLAgainstSamba run.sh SYS_ADMIN
TestEndToEnd run.sh -
TestExistingDataUntouched run.sh -
TestMissingDisk run.sh -
TestHostsAllow run.sh NET_ADMIN
TestOddPassword run.sh -
TestSambaOnlyLogin run.sh -
TestForeignSambaBackedUp run.sh -
TestDiscoveryInterfaces run.sh NET_ADMIN
TestEndToEndACL run.sh SYS_ADMIN'
# The scenarios that may have NET_ADMIN: a public source address, and a second interface.
NET_ADMIN_TESTS='TestHostsAllow TestDiscoveryInterfaces'
LOG=${TEST_ALPINE_LOG:-}
if [ -n "$LOG" ]; then
  : >"$LOG"
fi
# say TEXT: a line of the script's own on stdout and in the log.
say() {
  printf '%s\n' "$1"
  if [ -n "$LOG" ]; then printf '%s\n' "$1" >>"$LOG"; fi
}
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$("$GO" env GOARCH)" "$GO" test -c -tags integration \
  -o "$WORK/integration.test" ./integration)
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$("$GO" env GOARCH)" "$GO" build -trimpath \
  -o "$WORK/edelweiss-ext-samba" .)
# The packages: those of requires.apk in manifest.json (samba, avahi, dbus, wsdd), then OpenRC with the init scripts of
# busybox (syslog, crond), smbclient and smbcacls (samba-client), and sshd (openssh). apk update first, as every host has
# run it once, so that apk search in the tests reads an index.
cat >"$WORK/setup.sh" <<'EOF'
#!/bin/sh
set -eu
apk update --quiet >/dev/null
apk add --quiet samba avahi dbus wsdd openrc busybox-openrc samba-client openssh >/dev/null
mkdir -p /run/openrc
touch /run/openrc/softlevel
rc-service syslog start >/tmp/setup-rc.log 2>&1 || { cat /tmp/setup-rc.log; exit 1; }
EOF
# run.sh TEST: the test as root, with the data disk given to root; the script ends with the status of the test.
cat >"$WORK/run.sh" <<'EOF'
#!/bin/sh
chown 0:0 /srv/data && chmod 0755 /srv/data || exit 1
exec /t/integration.test -test.run "^$1\$" -test.v -test.count=1 -test.timeout 30m
EOF
chmod 0755 "$WORK" "$WORK/integration.test" "$WORK/edelweiss-ext-samba" "$WORK/setup.sh" "$WORK/run.sh"
failed=0
ran=0
# scenario IMAGE TEST SCRIPT CAPS SETUP: one test in a fresh container that SETUP has set up, run by SCRIPT with the
# capabilities CAPS and a fresh data disk (an anonymous volume); the test name reaches the container's shell as $1, the
# script as $2 and the setup as $3.
scenario() {
  opts=--init
  if [ "$4" != - ]; then
    for c in $(printf '%s' "$4" | tr ',' ' '); do
      opts="$opts --cap-add=$c"
    done
  fi
  ran=$((ran + 1))
  say "== $2 on $1"
  start=$(date +%s)
  rm -f "$OUT/rc" "$OUT/log"
  # $opts is split on purpose: it is a list of options.
  # shellcheck disable=SC2086
  { docker run --rm $opts -e EDELWEISS_ALPINE_TEST=1 -v "$WORK:/t:ro" -v /srv/data "$1" /bin/sh -c \
    '"/t/$3" && exec "/t/$2" "$1"' _ "$2" "$3" "$5" \
    && echo 0 >"$OUT/rc" || echo "$?" >"$OUT/rc"; } 2>&1 | tee -a "$OUT/log" ${LOG:+"$LOG"}
  took=$(($(date +%s) - start))
  if [ "$(cat "$OUT/rc" 2>/dev/null)" != 0 ] || ! grep -q "^--- PASS: $2 (" "$OUT/log"; then
    say "== FAILED: $2 on $1 (${took} s)" >&2
    failed=1
  else
    say "== passed: $2 on $1 (${took} s)"
  fi
}
# wanted TEST: whether the test is among the arguments (every test when there are none) and matches TESTS.
wanted() {
  case "$1" in *"${TESTS:-}"*) ;; *) return 1 ;; esac
  [ "$#" -eq 1 ] && return 0
  name=$1
  shift
  for a in "$@"; do
    [ "$a" = "$name" ] && return 0
  done
  return 1
}
printf '%s\n' "$SCENARIOS" >"$OUT/table"
# The whole table is checked before the first container starts: a script that is not in the work directory, a
# capability other than NET_ADMIN and SYS_ADMIN, NET_ADMIN for a test that is not in NET_ADMIN_TESTS, SYS_ADMIN for a
# test that is not a folder-rights test, or a fifth column, stops the script.
while read -r test script caps setup extra; do
  if [ -n "${extra:-}" ]; then
    say "test-alpine: $test: the line has a fifth column ($extra)" >&2
    exit 1
  fi
  for s in "$script" "${setup:-setup.sh}"; do
    if [ ! -f "$WORK/$s" ]; then
      say "test-alpine: $test: there is no script $s" >&2
      exit 1
    fi
  done
  for c in $(printf '%s' "$caps" | tr ',' ' '); do
    case "$c" in
    -) ;;
    NET_ADMIN)
      case " $NET_ADMIN_TESTS " in
      *" $test "*) ;;
      *)
        say "test-alpine: $test: NET_ADMIN is only for $NET_ADMIN_TESTS" >&2
        exit 1
        ;;
      esac
      ;;
    SYS_ADMIN)
      case "$test" in
      *ACL*) ;;
      *)
        say "test-alpine: $test: SYS_ADMIN is only for the folder-rights tests (a name with ACL)" >&2
        exit 1
        ;;
      esac
      ;;
    *)
      say "test-alpine: $test: the capability $c is not allowed" >&2
      exit 1
      ;;
    esac
  done
done <"$OUT/table"
while read -r test script caps setup _; do
  wanted "$test" "$@" || continue
  for image in $IMAGES; do
    scenario "$image" "$test" "$script" "$caps" "${setup:-setup.sh}"
  done
done <"$OUT/table"
if [ "$ran" -eq 0 ]; then
  say "test-alpine: no scenario matches the arguments ($*) and TESTS=${TESTS:-}" >&2
  exit 1
fi
say "test-alpine: $ran scenarios, $([ "$failed" = 0 ] && echo "all passed" || echo "failures above")"
exit "$failed"
