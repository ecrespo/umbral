#!/usr/bin/env bash
# Runs a command — normally the test suite — and fails if it was not hermetic.
#
# Three things a test run must never do, each of them found happening on 2026-09-20
# (docs/checkpoints/2026-09-20-f0-closure.md):
#
#   1. leave an `umbrald` behind. The daemon outlives its clients by design (REQ-TERM-003)
#      and there is no `system.shutdown`, so a test that autostarts one and closes its
#      connections has stopped nothing. Four were found alive on the development machine,
#      the oldest 56 minutes old.
#   2. leave temporary directories behind. `t.TempDir` cleans up unless the binary is
#      killed, and a killed daemon cannot remove its shell bootstrap directory: 2515 of
#      those had accumulated, 168 MB.
#   3. write to the developer's real database. One test overrode `XDG_RUNTIME_DIR` and
#      nothing else, so its daemon inherited the real `XDG_DATA_HOME` and wrote 2490
#      sessions into `~/.local/share/umbral/umbral.db`.
#
# None of the three showed in CI, because a runner's home is thrown away and its processes
# die with the job. That is precisely why this runs locally as well: the gate has to fail
# where the damage happens.
#
# Everything is measured as a **difference** across the run, not as an absolute, so a
# developer who already has a daemon or unrelated temp directories is not accused of a leak
# they did not cause.
#
# Run by `task test:hygiene`. Its teeth are `scripts/test_hygiene_selftest.sh`.
set -euo pipefail

cd "$(dirname "$0")/.."

# Overridable so the selftest can inject a violation without touching the real database or
# hunting real daemons. Nothing else should set these.
PROC_PATTERN="${UMBRAL_HYGIENE_PROC:-umbrald}"
DB_PATH="${UMBRAL_HYGIENE_DB:-${XDG_DATA_HOME:-$HOME/.local/share}/umbral/umbral.db}"
TMP_DIR="${TMPDIR:-/tmp}"

# `pgrep -u` so another user's processes on a shared machine are never counted, and `|| true`
# because pgrep exits 1 when nothing matches, which is the ordinary case.
processes() { pgrep -u "$(id -u)" -f "$PROC_PATTERN" 2>/dev/null | sort || true; }

# The two shapes a leak takes in the temporary directory: our own bootstrap directories,
# whose name is unambiguous, and Go's `t.TempDir` leftovers, which only survive a killed
# binary.
temp_dirs() {
  find "$TMP_DIR" -maxdepth 1 \( -name 'umbral-shellinteg-*' -o -name 'Test*' \) \
    -newermt '1970-01-01' 2>/dev/null | sort || true
}

# mtime and size together: a write that happens to preserve the mtime still moves the size,
# and a write that preserves both wrote nothing worth reporting.
db_fingerprint() {
  if [ -e "$DB_PATH" ]; then
    stat -c '%Y %s' "$DB_PATH" 2>/dev/null || stat -f '%m %z' "$DB_PATH" 2>/dev/null || echo "unreadable"
  else
    echo "absent"
  fi
}

before_procs=$(processes)
before_dirs=$(temp_dirs)
before_db=$(db_fingerprint)
# A daemon that was already running may legitimately write to that database while the suite
# runs, so the database check becomes a warning rather than a failure in that case. The other
# two checks still bite.
db_check_is_advisory=0
[ -n "$before_procs" ] && db_check_is_advisory=1

echo "test:hygiene: running '$*'"
set +e
"$@"
command_status=$?
set -e

# A moment for processes that are on their way out: a daemon signalled as the test binary
# exits has not necessarily been reaped by the time this line runs, and reporting it would be
# a false accusation that teaches people to ignore the gate.
sleep 1

after_procs=$(processes)
after_dirs=$(temp_dirs)
after_db=$(db_fingerprint)

failed=0

leaked_procs=$(comm -13 <(printf '%s\n' "$before_procs") <(printf '%s\n' "$after_procs") | sed '/^$/d')
if [ -n "$leaked_procs" ]; then
  failed=1
  echo "test:hygiene: FAIL — the run left $(printf '%s\n' "$leaked_procs" | wc -l) '$PROC_PATTERN' process(es) behind:" >&2
  while read -r pid; do
    [ -n "$pid" ] && ps -o pid=,etimes=,cmd= -p "$pid" 2>/dev/null >&2
  done <<< "$leaked_procs"
  echo "  A test that starts a daemon must own it and stop it: umbrald outlives its clients" >&2
  echo "  (REQ-TERM-003) and there is no system.shutdown." >&2
fi

leaked_dirs=$(comm -13 <(printf '%s\n' "$before_dirs") <(printf '%s\n' "$after_dirs") | sed '/^$/d')
if [ -n "$leaked_dirs" ]; then
  failed=1
  echo "test:hygiene: FAIL — the run left $(printf '%s\n' "$leaked_dirs" | wc -l) directory/directories in $TMP_DIR:" >&2
  printf '%s\n' "$leaked_dirs" | head -10 | sed 's/^/    /' >&2
  [ "$(printf '%s\n' "$leaked_dirs" | wc -l)" -gt 10 ] && echo "    … and more" >&2
fi

if [ "$before_db" != "$after_db" ]; then
  if [ "$db_check_is_advisory" -eq 1 ]; then
    echo "test:hygiene: note — $DB_PATH changed, but a daemon was already running before the" >&2
    echo "  run and may own that write. Not failing on it." >&2
  else
    failed=1
    echo "test:hygiene: FAIL — the run wrote to the real database at $DB_PATH" >&2
    echo "  was: $before_db" >&2
    echo "  now: $after_db" >&2
    echo "  A test that starts a daemon must redirect every XDG_* directory it writes to," >&2
    echo "  not only XDG_RUNTIME_DIR: that one moves the socket and leaves the database" >&2
    echo "  exactly where the developer's own is." >&2
  fi
fi

if [ "$failed" -ne 0 ]; then
  exit 1
fi

if [ "$command_status" -ne 0 ]; then
  echo "test:hygiene: the command failed (exit $command_status); it left nothing behind"
  exit "$command_status"
fi

echo "test:hygiene: OK — no daemon, no temporary directory and no write to $DB_PATH"
