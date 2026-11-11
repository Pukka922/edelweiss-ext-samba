#!/bin/sh
# Prepares the edelweiss dev container for trying the Shares page ("Freigaben") in the browser. Run it in the edelweiss
# checkout while the dev environment runs with the compose overlay of this repository (dev/compose.samba.yaml), as root in
# the container; the README (Development, "Trying it in the edelweiss dev environment") has the whole recipe:
#
#   docker compose -f dev/compose.yaml -f ../edelweiss-ext-samba/dev/compose.samba.yaml exec -T edelweiss sh -s \
#     < ../edelweiss-ext-samba/dev/samba-dev.sh
#
# It does NOT set Samba up: the browser test starts with the button "Samba einrichten" of the page. It prepares what the
# setup needs and what edelweiss checks before it shows the page:
#
#   - OpenRC (apk add openrc) with /run/openrc/softlevel (an empty one is enough: no OpenRC is booted), so that the setup
#     can run rc-update and rc-service; rc_cgroup_mode="none" (no cgroups for the services) and rc_controller_cgroups="NO"
#     (no cgroup controllers mounted) in /etc/rc.conf, as the cgroup tree of a container is read-only (OpenRC would print
#     mkdir errors at every start and stop);
#   - the init scripts of busybox (apk add busybox-openrc), and busybox syslogd started as OpenRC's service syslog, which
#     writes /var/log/messages as on an Alpine host: the setup starts crond, whose service needs the service syslog, and
#     OpenRC does not count a syslogd that it did not start. A syslogd that runs without OpenRC is stopped first, and a
#     service that OpenRC has as crashed is reset to stopped;
#   - the packages of requires.apk in manifest.json (samba, avahi, dbus, wsdd), so that edelweiss finds the requirements
#     of the extension met. They are installed here, as the dev container is no host to run apk add on by hand.
#
# It can run again, before the setup and after it: apk add does nothing for what is there, and the service syslog is
# started only when OpenRC does not have it started. It touches nothing else.
#
# It refuses to run anywhere but in the edelweiss dev container (see guard, which accepts PID 1 edelweissd and docker's init
# with edelweissd as its child): on a host it would write the settings of a container into /etc/rc.conf. It changes the
# dev container only, which make dev recreates.
set -eu

PACKAGES="samba avahi dbus wsdd"

# guard: ends the script, having changed nothing, unless it runs in the edelweiss dev container: a container
# (/.dockerenv or /run/.containerenv) with one of two layouts, and no other:
#   - PID 1 is edelweissd (the entrypoint of the dev container execs it);
#   - PID 1 is docker's init (docker-init, or tini where that is its name), which init: true of dev/compose.samba.yaml
#     makes, and a process named edelweissd has PID 1 as its parent.
guard() {
  if [ ! -e /.dockerenv ] && [ ! -e /run/.containerenv ]; then
    echo "samba-dev: this is no container (neither /.dockerenv nor /run/.containerenv exists): nothing done" >&2
    exit 1
  fi
  pid1=$(cat /proc/1/comm 2>/dev/null) || pid1=
  case "$pid1" in
  edelweissd) return 0 ;;
  docker-init | tini) ;;
  *)
    echo "samba-dev: PID 1 is ${pid1:-unknown}, neither edelweissd nor docker's init (docker-init, tini) as in the" \
      "edelweiss dev container: nothing done" >&2
    exit 1
    ;;
  esac
  for p in /proc/[0-9]*; do
    [ "$(cat "$p/comm" 2>/dev/null)" = edelweissd ] || continue
    [ "$(sed -n 's/^PPid:[[:space:]]*//p' "$p/status" 2>/dev/null)" = 1 ] && return 0
  done
  echo "samba-dev: PID 1 is $pid1, but no process named edelweissd has it as its parent, so this is not the edelweiss dev" \
    "container: nothing done" >&2
  exit 1
}

# start_syslog: starts busybox syslogd as OpenRC's service syslog, unless OpenRC has it started. A syslogd that runs
# without OpenRC is stopped first, so that one syslogd runs: the one of the service (OpenRC would not start the service
# beside it: start-stop-daemon finds a process of that name). It is looked for as OpenRC does, by the name of the process:
# pidof, not pgrep -x, which in busybox matches argv[0] as it is. A service that OpenRC has as started while its syslogd
# is gone (crashed) is reset to stopped first: its start would do nothing.
start_syslog() {
  st=0
  rc-service syslog status >/dev/null 2>&1 || st=$?
  if [ "$st" = 0 ]; then
    return 0
  fi
  if [ "$st" = 32 ]; then
    echo "samba-dev: the service syslog has crashed: resetting it to stopped"
    rc-service syslog zap >/dev/null 2>&1 || true
  fi
  if pids=$(pidof syslogd); then
    echo "samba-dev: stopping the syslogd that runs without OpenRC (pid $pids)"
    # $pids is split on purpose: it is a list of pids.
    # shellcheck disable=SC2086
    kill $pids 2>/dev/null || true
    i=0
    while pidof syslogd >/dev/null; do
      i=$((i + 1))
      if [ "$i" -gt 50 ]; then
        echo "samba-dev: the syslogd that runs without OpenRC does not stop: stop it by hand" >&2
        exit 1
      fi
      sleep 0.1
    done
  fi
  rc-service syslog start || {
    echo "samba-dev: rc-service syslog start failed: crond, which the setup starts, needs the service syslog" >&2
    exit 1
  }
}

main() {
  guard
  # $PACKAGES is split on purpose: it is a list of package names.
  # shellcheck disable=SC2086
  apk add --no-cache --quiet openrc busybox-openrc $PACKAGES
  grep -q '^rc_cgroup_mode="none"$' /etc/rc.conf ||
    printf 'rc_cgroup_mode="none"\nrc_controller_cgroups="NO"\n' >>/etc/rc.conf
  mkdir -p /run/openrc
  touch /run/openrc/softlevel
  start_syslog
  echo "samba-dev: ready: OpenRC, the service syslog and the packages $PACKAGES; Samba is not set up: run the setup of the page"
}

# The whole script is read before main runs, and main reads nothing from stdin: sh -s reads the script from stdin.
main </dev/null
