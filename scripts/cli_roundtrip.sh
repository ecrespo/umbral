#!/usr/bin/env bash
# F0 exit criterion 4, performed rather than argued: "a script creates a workspace, splits,
# exports and reapplies a layout using only the CLI" (REQ-CLI-005, REQ-CLI-006, T-F0-20).
#
# Against a real `umbrald`, not a fake: the gap that blocked T-F0-20 for a day — API §2 did
# not let the `cli` client kind call the tree at all — was invisible to every test that used
# a fake daemon, and would have failed here on the first command.
#
# Hermetic like the test suite (AGENTS.md): every XDG_* directory and HOME point into a
# directory this script owns, the daemon it starts is the one it stops, and nothing is left
# behind. `umb` runs with --no-autostart, so a mistake cannot quietly start a second daemon.
#
# Run by `task roundtrip`, which builds the binaries first. UMBRALD and UMB override them.
set -euo pipefail

cd "$(dirname "$0")/.."

UMBRALD="${UMBRALD:-bin/umbrald}"
UMB="${UMB:-bin/umb}"
for bin in "$UMBRALD" "$UMB"; do
  [ -x "$bin" ] || { echo "cli_roundtrip: $bin is not built; run \`task build\`" >&2; exit 1; }
done
UMBRALD="$(cd "$(dirname "$UMBRALD")" && pwd)/$(basename "$UMBRALD")"
UMB="$(cd "$(dirname "$UMB")" && pwd)/$(basename "$UMB")"

# Under /tmp rather than $TMPDIR on purpose: a Unix socket path is limited to about a hundred
# bytes, and macOS's $TMPDIR alone takes half of that before the socket's own directories.
# XDG_RUNTIME_DIR below wins over the platform default on every OS (API Spec §2), so the socket
# is in the same place on Linux and macOS, and both binaries are also told so with --socket.
WORK="$(mktemp -d /tmp/umbral-rt.XXXXXX)"
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

mkdir -p "$WORK/run" "$WORK/data" "$WORK/config" "$WORK/state" "$WORK/cache" "$WORK/home" \
  "$WORK/repo" "$WORK/other"
chmod 700 "$WORK/run"
export XDG_RUNTIME_DIR="$WORK/run" XDG_DATA_HOME="$WORK/data" XDG_CONFIG_HOME="$WORK/config" \
  XDG_STATE_HOME="$WORK/state" XDG_CACHE_HOME="$WORK/cache" HOME="$WORK/home"
unset UMBRAL_SESSION_ID

fail() { echo "cli_roundtrip: FAIL — $*" >&2; [ -f "$WORK/daemon.log" ] && tail -20 "$WORK/daemon.log" >&2; exit 1; }
# The shared flags go right after the family and subcommand, never at the end: anything after
# a `--` is the pane's command, and a trailing --no-autostart would be stored in it — and
# would leave that one call free to autostart a daemon this script does not own.
umb() { local fam=$1 sub=$2; shift 2; "$UMB" "$fam" "$sub" --no-autostart --socket "$SOCKET" "$@"; }
# field FILE KEY...: one value out of a JSON answer, with python3 because `task specs` already
# needs it and jq is not a dependency of this repo. `#` as the last key prints a length.
field() {
  python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))
for key in sys.argv[2:]:
    d = len(d) if key == "#" else d[key]
print(d)' "$@"
}

SOCKET="$XDG_RUNTIME_DIR/umbral/umbral.sock"

"$UMBRALD" --socket "$SOCKET" >"$WORK/daemon.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 100); do [ -S "$SOCKET" ] && break; sleep 0.1; done
[ -S "$SOCKET" ] || fail "umbrald did not open $SOCKET"

# 1. A workspace — its tab and root pane come back in the same answer.
umb workspace create "$WORK/repo" --label repo --json >"$WORK/ws1.json" || fail "workspace create"
W1=$(field "$WORK/ws1.json" workspace id)
T1=$(field "$WORK/ws1.json" tab id)
P1=$(field "$WORK/ws1.json" root_pane id)
echo "── workspace $W1, tab $T1, pane $P1"

# 2. Splits, addressed positionally — `umb pane split w1:p1` is the command Art. 6's
#    exception is written for. The second carries a command, which the round trip must keep.
umb pane split "$P1" --direction right --ratio 0.6 --json >"$WORK/split1.json" || fail "pane split $P1"
P2=$(field "$WORK/split1.json" pane id)
umb pane rename "$P1" editor >/dev/null || fail "pane rename $P1"
umb pane split "$P2" --direction down --json -- sh -c 'sleep 600' >"$WORK/split2.json" || fail "pane split $P2 with a command"
P3=$(field "$WORK/split2.json" pane id)
[ "$(field "$WORK/split2.json" pane command)" = "['sh', '-c', 'sleep 600']" ] \
  || fail "the split's command is not exactly sh -c 'sleep 600': $(field "$WORK/split2.json" pane command)"
umb pane rename "$P3" watcher >/dev/null || fail "pane rename $P3"
echo "── split into $P1, $P2 and $P3"

# 3. Export to a file, the way the criterion's script would keep it.
umb layout export "$T1" --json >"$WORK/layout.json" || fail "layout export $T1"

# 4. A second workspace, and the layout applied into it through a pipe (REQ-CLI-006).
umb workspace create "$WORK/other" --json >"$WORK/ws2.json" || fail "workspace create (second)"
W2=$(field "$WORK/ws2.json" workspace id)
umb layout export "$T1" --json | umb layout apply "$W2" --from - --json >"$WORK/applied.json" \
  || fail "layout export | layout apply"
T2=$(field "$WORK/applied.json" tab id)
echo "── applied into $W2 as $T2"

# 5. The second tab's tree matches the first: same shape, splits, ratios, labels, cwds,
#    commands. Pane identifiers differ by construction and are the only thing ignored.
umb layout export "$T2" --json >"$WORK/layout2.json" || fail "layout export $T2"
python3 - "$WORK/layout.json" "$WORK/layout2.json" <<'PY' || fail "the applied tab does not reproduce the exported one"
import json, sys

def portable(node):
    node = {k: v for k, v in node.items() if k != "pane_id"}
    for child in ("first", "second"):
        if child in node:
            node[child] = portable(node[child])
    return node

a = portable(json.load(open(sys.argv[1]))["root"])
b = portable(json.load(open(sys.argv[2]))["root"])
if a != b:
    print("exported:", json.dumps(a, sort_keys=True), file=sys.stderr)
    print("applied: ", json.dumps(b, sort_keys=True), file=sys.stderr)
    sys.exit(1)
PY

# 6. The command came back pending, never run (REQ-TERM-011), and the user was told.
python3 - "$WORK/applied.json" <<'PY' || fail "the applied layout ran its command or hid the warning"
import json, sys
d = json.load(open(sys.argv[1]))
with_command = [p for p in d["panes"] if p.get("command")]
assert with_command, "no applied pane carries the command"
assert all(p.get("command_pending") for p in with_command), "a command was not left pending"
assert any("pending" in w for w in d["warnings"]), d["warnings"]
PY

# 7. The tree is what the CLI says it is, read back through it.
umb tab list "$W2" --json >"$WORK/tabs.json" || fail "tab list $W2"
python3 - "$WORK/tabs.json" "$T2" <<'PY' || fail "tab list $W2 does not include the applied tab $T2"
import json, sys
assert sys.argv[2] in [t["id"] for t in json.load(open(sys.argv[1]))["items"]]
PY

echo
echo "cli_roundtrip: OK — a workspace created, split, exported and reapplied using only the CLI"
