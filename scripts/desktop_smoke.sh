#!/usr/bin/env bash
# The desktop client end to end (REQ-DSK-001): a real `umbrald`, the Wails window under a
# virtual display, and the page itself typing `echo smoke-$((<marker>+1))` into its first pane and
# reporting once the sum, which only the shell can print, is drawn. `internal/desktop` is tested against a real daemon by
# the suite; this is what proves the page, the bindings and the webview work on top of it.
#
# Hermetic like `cli_roundtrip.sh`: every XDG_* directory and HOME point into a directory this
# script owns, and the daemon it starts is the one it stops.
#
# Needs xvfb-run and dbus-run-session. UMBRALD and DESKTOP name the binaries.
set -euo pipefail

cd "$(dirname "$0")/.."

UMBRALD="${UMBRALD:-bin/umbrald}"
DESKTOP="${DESKTOP:-bin/umbral-desktop}"
for bin in "$UMBRALD" "$DESKTOP"; do
  [ -x "$bin" ] || { echo "desktop_smoke: $bin is not built" >&2; exit 1; }
done
UMBRALD="$(cd "$(dirname "$UMBRALD")" && pwd)/$(basename "$UMBRALD")"
DESKTOP="$(cd "$(dirname "$DESKTOP")" && pwd)/$(basename "$DESKTOP")"

WORK="$(mktemp -d /tmp/umbral-desk.XXXXXX)"
DAEMON_PID=""
cleanup() {
  if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill -INT "$DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 50); do kill -0 "$DAEMON_PID" 2>/dev/null || break; sleep 0.1; done
    kill -KILL "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

mkdir -p "$WORK/run" "$WORK/data" "$WORK/config" "$WORK/state" "$WORK/cache" "$WORK/home"
chmod 700 "$WORK/run"
export XDG_RUNTIME_DIR="$WORK/run" XDG_DATA_HOME="$WORK/data" XDG_CONFIG_HOME="$WORK/config" \
  XDG_STATE_HOME="$WORK/state" XDG_CACHE_HOME="$WORK/cache" HOME="$WORK/home"
unset UMBRAL_SESSION_ID

fail() { echo "desktop_smoke: FAIL — $*" >&2; [ -f "$WORK/daemon.log" ] && tail -20 "$WORK/daemon.log" >&2; exit 1; }

SOCKET="$XDG_RUNTIME_DIR/umbral/umbral.sock"
"$UMBRALD" --socket "$SOCKET" >"$WORK/daemon.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 100); do [ -S "$SOCKET" ] && break; sleep 0.1; done
[ -S "$SOCKET" ] || fail "umbrald did not open $SOCKET"

# The page types `echo smoke-$((MARKER+1))`: a leading 1, so the shell never reads it as octal.
MARKER="1$RANDOM$RANDOM"
ANSWER=$((MARKER + 1))
# The window quits itself once the page has drawn the marker, and exits 1 after 90 s without it.
UMBRAL_DESKTOP_SMOKE="$MARKER" GTK_A11Y=none \
  timeout 120 xvfb-run -a dbus-run-session -- "$DESKTOP" >"$WORK/desktop.log" 2>&1 || {
    cat "$WORK/desktop.log" >&2
    fail "the window did not draw smoke-$ANSWER"
  }
grep -q "smoke: drawn" "$WORK/desktop.log" || { cat "$WORK/desktop.log" >&2; fail "the window quit without drawing"; }
echo "desktop_smoke: the window drew smoke-$ANSWER, the shell's answer, from a real daemon"
