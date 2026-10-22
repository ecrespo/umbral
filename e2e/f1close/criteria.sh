#!/usr/bin/env bash
# F1 "Done" criteria 4 and 5, measured against an isolated daemon built from the tree, plus the
# daemon's egress_log count for criterion 3 (whose main evidence is the US-003 run's report):
#   4. a shell script drives a full thread with `umb thread send --wait` and `umb thread wait`,
#      no polling (REQ-CLI-009);
#   5. an external process reports state, appears in the rollup and releases authority.
# Every XDG directory and HOME point into a throwaway directory; the daemon is stopped and the
# directory removed on exit. Any state other than the expected one exits non-zero.
set -euo pipefail
REPO=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
ROOT=$(mktemp -d /tmp/umb-f1.XXXXXX)
PID=
cleanup() {
	[[ -n $PID ]] && kill "$PID" 2>/dev/null && wait "$PID" 2>/dev/null || true
	rm -rf "$ROOT"
}
trap cleanup EXIT

export XDG_RUNTIME_DIR=$ROOT/run XDG_DATA_HOME=$ROOT/data XDG_CONFIG_HOME=$ROOT/config \
	XDG_STATE_HOME=$ROOT/state XDG_CACHE_HOME=$ROOT/cache HOME=$ROOT/home
mkdir -p "$XDG_RUNTIME_DIR/umbral" "$XDG_DATA_HOME" "$XDG_CONFIG_HOME/umbral" "$XDG_STATE_HOME" "$XDG_CACHE_HOME" "$HOME" "$ROOT/bin" "$ROOT/work"
chmod 700 "$XDG_RUNTIME_DIR" "$XDG_RUNTIME_DIR/umbral"
cat >"$XDG_CONFIG_HOME/umbral/models.toml" <<'EOF'
[router]
offline = true

[classes]
code = ["ollama/gpt-oss:20b"]
fast = ["ollama/gpt-oss:20b"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = "http://127.0.0.1:11434"
[providers.options]
num_ctx = 16384
keep_alive = "30m"
EOF

(cd "$REPO" && go build -o "$ROOT/bin/umbrald" ./cmd/umbrald && go build -o "$ROOT/bin/umb" ./cmd/umb)
"$ROOT/bin/umbrald" >"$ROOT/daemon.log" 2>&1 &
PID=$!
SOCK=$XDG_RUNTIME_DIR/umbral/umbral.sock
for _ in $(seq 100); do [[ -S $SOCK ]] && break; sleep 0.1; done
[[ -S $SOCK ]] || { cat "$ROOT/daemon.log"; exit 1; }
"$ROOT/bin/umb" version
echo "daemon pid $PID, runtime $XDG_RUNTIME_DIR"

UMB() { "$ROOT/bin/umb" "$@" --no-autostart; }
expect() { [[ $2 == "$3" ]] || { echo "FAIL: $1: got '$2', want '$3'" >&2; cat "$ROOT/daemon.log" >&2; exit 1; }; }
rollup() { UMB workspace list --json | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["rollup_state"])'; }
pane_state() { UMB pane get "$PANE" --json | python3 -I -c 'import json,sys; p=json.load(sys.stdin); p=p.get("pane", p); print(p["attention_state"])'; }

echo "== criterion 4: a script drives a thread with umb alone =="
UNTIL=idle,done,blocked,stopped
THREAD=$(UMB thread create "$ROOT/work" --title "f1 criterion 4")
read -r TURN STATE < <(UMB thread send "$THREAD" "Reply with the single word pong and nothing else." --wait "$UNTIL")
echo "send --wait: $TURN $STATE"
expect "send --wait" "$STATE" done
TURN=$(UMB thread send "$THREAD" "Now reply with the single word ping.")
STATE=$(UMB thread wait "$THREAD" --until "$UNTIL")
echo "send, then wait: $TURN $STATE"
expect "send, then wait" "$STATE" done
UMB thread get "$THREAD" | tee "$ROOT/thread.txt"
grep -q '^assistant: .*pong' "$ROOT/thread.txt" || expect "first answer" "missing" pong
grep -q '^assistant: .*ping' "$ROOT/thread.txt" || expect "second answer" "missing" ping

echo "== criterion 5: an external report drives the rollup and is released =="
UMB workspace create "$ROOT/work" --label f1check >/dev/null
UMB workspace list --json | python3 -I -c 'import json,sys; [print("before:", w["id"], w["rollup_state"]) for w in json.load(sys.stdin)["items"]]'
BEFORE=$(rollup)
PANE=$(UMB pane list w1:t1 --json | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["id"])')
UMB pane report_state blocked "$PANE" --source ext:f1check --agent checker --message "needs a decision"
UMB pane get "$PANE" --json | python3 -I -c 'import json,sys; p=json.load(sys.stdin); p=p.get("pane", p); print("pane:", p["id"], p["attention_state"], p["state_source"])'
UMB workspace list --json | python3 -I -c 'import json,sys; [print("reported:", w["id"], w["rollup_state"]) for w in json.load(sys.stdin)["items"]]'
expect "pane after the report" "$(pane_state)" blocked
expect "rollup after the report" "$(rollup)" blocked
UMB pane release_state "$PANE" --source ext:f1check
UMB pane get "$PANE" --json | python3 -I -c 'import json,sys; p=json.load(sys.stdin); p=p.get("pane", p); print("pane:", p["id"], p["attention_state"], p["state_source"])'
UMB workspace list --json | python3 -I -c 'import json,sys; [print("released:", w["id"], w["rollup_state"]) for w in json.load(sys.stdin)["items"]]'
expect "rollup after the release" "$(rollup)" "$BEFORE"
EGRESS=$(python3 -I -c 'import sqlite3,sys; print(sqlite3.connect(sys.argv[1]).execute("select count(*) from egress_log").fetchone()[0])' "$XDG_DATA_HOME/umbral/umbral.db")
echo "== egress_log rows: $EGRESS =="
expect "egress_log rows" "$EGRESS" 0
echo "criteria: OK"
