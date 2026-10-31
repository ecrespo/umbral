#!/usr/bin/env bash
# The Windows binaries against each other, on CI's windows job (T-F2-50, REQ-WIN-001, delta
# `2026-10-windows-port` decisions 3, 11 and 13): `umbrald.exe` serves its named pipe, `umb.exe`
# reaches it and creates a workspace with a ConPTY shell, every `remote.*` method answers
# NOT_IMPLEMENTED, and `--takeover` exits EX_UNAVAILABLE (69) before touching anything.
#
# Hermetic like cli_roundtrip.sh: every XDG_* directory points into a directory this script owns,
# `umb` never autostarts, and the daemon it starts is the one it stops. Run under Git Bash.
#
# UMBRALD and UMB name the binaries.
set -euo pipefail

UMBRALD="${UMBRALD:?set UMBRALD to umbrald.exe}"
UMB="${UMB:?set UMB to umb.exe}"

WORK="$(mktemp -d)"
DAEMON_PID=""
cleanup() {
  if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK" || true
}
trap cleanup EXIT

mkdir -p "$WORK/run" "$WORK/data" "$WORK/config" "$WORK/state" "$WORK/cache" "$WORK/repo"
# Windows paths, since the daemon reads them and Git Bash's /tmp means nothing to it.
win() { cygpath -w "$1"; }
export XDG_RUNTIME_DIR="$(win "$WORK/run")" XDG_DATA_HOME="$(win "$WORK/data")" \
  XDG_CONFIG_HOME="$(win "$WORK/config")" XDG_STATE_HOME="$(win "$WORK/state")" \
  XDG_CACHE_HOME="$(win "$WORK/cache")"
unset UMBRAL_SESSION_ID SHELL
SOCKET="$XDG_RUNTIME_DIR\\umbral\\umbral.sock"

fail() { echo "windows_smoke: FAIL — $*" >&2; [ -f "$WORK/daemon.log" ] && tail -30 "$WORK/daemon.log" >&2; exit 1; }
umb() { local fam=$1 sub=$2; shift 2; "$UMB" "$fam" "$sub" --no-autostart --socket "$SOCKET" "$@"; }

# 1. A live handoff is refused on Windows before anything is opened.
set +e
"$UMBRALD" --takeover --socket "$SOCKET" >"$WORK/takeover.log" 2>&1
code=$?
set -e
[ "$code" -eq 69 ] || { cat "$WORK/takeover.log" >&2; fail "umbrald --takeover exited $code, want 69"; }
grep -q "live handoff is not supported on Windows" "$WORK/takeover.log" \
  || fail "umbrald --takeover did not say why: $(cat "$WORK/takeover.log")"
echo "── --takeover refused with 69"

# 2. The daemon serves its pipe, and umb reaches it.
"$UMBRALD" --socket "$SOCKET" >"$WORK/daemon.log" 2>&1 &
DAEMON_PID=$!
up=""
for _ in $(seq 100); do
  if umb workspace list >/dev/null 2>&1; then up=1; break; fi
  kill -0 "$DAEMON_PID" 2>/dev/null || fail "umbrald exited before serving"
  sleep 0.1
done
[ -n "$up" ] || fail "umb never reached umbrald over its pipe"
echo "── umb reached umbrald over the pipe"

# 3. A workspace, whose root pane is a ConPTY shell.
umb workspace create "$(win "$WORK/repo")" --label repo --json >"$WORK/ws.json" \
  || fail "workspace create"
grep -q '"root_pane"' "$WORK/ws.json" || fail "workspace create answered $(cat "$WORK/ws.json")"
echo "── workspace created"

# 4. Remote hosts are not wired on Windows: the method answers NOT_IMPLEMENTED.
set +e
umb remote list >"$WORK/remote.out" 2>"$WORK/remote.err"
code=$?
set -e
[ "$code" -ne 0 ] || fail "umb remote list succeeded on Windows: $(cat "$WORK/remote.out")"
grep -q "NOT_IMPLEMENTED" "$WORK/remote.err" || fail "umb remote list said $(cat "$WORK/remote.err")"
echo "── remote.list answered NOT_IMPLEMENTED"

echo "windows_smoke: PASS"
