#!/usr/bin/env bash
# Runs umbrald from the .deb's systemd --user unit in an Ubuntu 26.04 container booted with
# systemd (T-PKG-06).
#
#   packaging/deb/smoke_systemd.sh umbral_<version>_<arch>.deb
#
# The container runs systemd as its init, so a lingering user gets a real user manager. As that
# user, `systemctl --user start umbrald` must serve `umb status` without autostarting another
# daemon and open a pane, a live handoff must be refused with the pane's shell untouched, and a
# stop must leave nothing in the unit's cgroup — the shell included. The unit must be installed and
# not enabled. The package is mounted under /srv: systemd mounts a tmpfs over /tmp. Booting systemd needs a privileged container sharing the host's cgroup tree.
set -euo pipefail

deb="${1:?usage: smoke_systemd.sh PATH_TO_DEB}"
image="${UMBRAL_SMOKE_IMAGE:-ubuntu:26.04}"
name="$(basename "$deb")"
box="umbral-smoke-systemd-$$"

trap 'docker stop "$box" >/dev/null 2>&1 || true' EXIT

docker run -d --rm --name "$box" --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  -v "$(cd "$(dirname "$deb")" && pwd)/$name:/srv/$name:ro" "$image" \
  bash -c 'export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq && apt-get install -y -qq systemd systemd-sysv dbus-user-session procps >/dev/null
    exec /sbin/init' >/dev/null

for _ in $(seq 1 100); do
  state="$(docker exec "$box" systemctl is-system-running 2>/dev/null || true)"
  [[ "$state" == running || "$state" == degraded ]] && break
  sleep 3
done
[[ "$state" == running || "$state" == degraded ]] || { echo "smoke: systemd did not boot ($state)" >&2; exit 1; }

docker exec -e DEB="/srv/$name" "$box" bash -euo pipefail -c '
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y -qq "$DEB" >/dev/null
  dpkg -L umbral | grep -qxF /usr/lib/systemd/user/umbrald.service

  useradd -m smoke
  loginctl enable-linger smoke
  for _ in $(seq 1 50); do
    systemctl is-active -q user@$(id -u smoke).service && break
    sleep 0.2
  done
  as_smoke() { runuser -u smoke -- env XDG_RUNTIME_DIR=/run/user/$(id -u smoke) "$@"; }

  # Installed, not enabled: the user decides.
  test "$(as_smoke systemctl --user is-enabled umbrald.service || true)" = disabled

  as_smoke systemctl --user start umbrald.service
  as_smoke systemctl --user is-active -q umbrald.service
  # --no-autostart: the status must come from the unit'"'"'s daemon, not one umb started.
  for _ in $(seq 1 50); do
    as_smoke umb status --no-autostart >/dev/null 2>&1 && break
    sleep 0.2
  done
  as_smoke umb status --no-autostart
  test "$(pgrep -u smoke -x umbrald | wc -l)" -eq 1

  # A pane: its shell is one of the unit'"'"'s processes.
  as_smoke umb workspace create /home/smoke --label smoke --no-autostart >/dev/null
  cgroup="/sys/fs/cgroup$(as_smoke systemctl --user show -p ControlGroup --value umbrald.service)"
  procs() { cat "$cgroup/cgroup.procs" 2>/dev/null | wc -l; }
  for _ in $(seq 1 50); do [ "$(procs)" -ge 2 ] && break; sleep 0.2; done
  before="$(procs)"
  [ "$before" -ge 2 ] || { echo "smoke: the pane'"'"'s shell is not in the unit ($before processes)" >&2; exit 1; }

  # A live handoff is refused under the unit and changes nothing.
  if takeover="$(as_smoke umbrald --takeover 2>&1)"; then
    echo "smoke: a handoff was given under systemd" >&2
    exit 1
  fi
  # Refused for the right reason, at hello, naming the restart: not a takeover that failed to
  # reach the daemon at all.
  case "$takeover" in
    *hello*"systemctl --user restart umbrald"*) ;;
    *) echo "smoke: the takeover failed, but not as a refusal under systemd: $takeover" >&2; exit 1 ;;
  esac
  as_smoke systemctl --user is-active -q umbrald.service
  as_smoke umb status --no-autostart >/dev/null
  [ "$(procs)" -eq "$before" ] || { echo "smoke: the refused handoff changed the unit'"'"'s processes" >&2; exit 1; }

  as_smoke systemctl --user stop umbrald.service
  if [ "$(procs)" -ne 0 ]; then
    echo "smoke: $(procs) processes left in the unit after it stopped" >&2
    exit 1
  fi
  if pgrep -u smoke -x umbrald >/dev/null; then
    echo "smoke: umbrald still runs after the unit stopped" >&2
    exit 1
  fi
  echo "smoke: systemd OK"
'
