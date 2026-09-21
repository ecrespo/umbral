#!/usr/bin/env bash
# Proves that `scripts/test_hygiene.sh` has teeth, by leaking each of the three things it
# exists to catch and asserting that it fails on every one.
#
# The sibling of arch_selftest.sh and perf_selftest.sh, and there for the same reason: a
# check that has quietly stopped checking passes forever, and this one is worse than most
# because its subject is *absence*. A hygiene gate that never fires looks identical to a
# suite that never leaks, and the second is what everyone will assume.
#
# Nothing here touches a real daemon or the real database: the script under test takes the
# process pattern and the database path from the environment precisely so this file can
# point them at a probe.
set -euo pipefail

cd "$(dirname "$0")/.."

WORK="$(mktemp -d)"
PROBE_NAME="umbral-hygiene-probe-$$"
cleanup() {
  pkill -u "$(id -u)" -f "$PROBE_NAME" 2>/dev/null || true
  rm -rf "$WORK" "${TMPDIR:-/tmp}/umbral-shellinteg-selftest-$$"
}
trap cleanup EXIT

export UMBRAL_HYGIENE_PROC="$PROBE_NAME"
export UMBRAL_HYGIENE_DB="$WORK/fake.db"
echo "a stand-in for the developer's database" > "$UMBRAL_HYGIENE_DB"

fail() { echo "test:hygiene:selftest: $1" >&2; exit 1; }

# --- 0. it passes when nothing leaks -------------------------------------------------
# Without this the three checks below would also pass on a script that always fails.
if ! ./scripts/test_hygiene.sh true >/dev/null 2>&1; then
  fail "the check fails on a clean run, so its failures mean nothing"
fi
echo "── clean run accepted"

# --- 1. a leaked process ---------------------------------------------------------------
# A script rather than a copy of `sleep`: coreutils ships as a multi-call binary that
# dispatches on argv[0], so a renamed copy exits at once with "unknown program" and the probe
# would never be running. And it sleeps rather than `exec sleep`, because exec replaces the
# process image and the probe's name — which is what `pgrep -f` matches on — disappears from
# the command line with it. Both mistakes were made here first; the teeth check needed its own
# teeth checking.
cat > "$WORK/$PROBE_NAME" <<PROBE_EOF
#!/usr/bin/env bash
sleep "\$1"
PROBE_EOF
chmod +x "$WORK/$PROBE_NAME"
if ./scripts/test_hygiene.sh bash -c "\"$WORK/$PROBE_NAME\" 30 &" >/dev/null 2>&1; then
  fail "a process left running was NOT caught"
fi
echo "── leaked process caught"
pkill -u "$(id -u)" -f "$PROBE_NAME" 2>/dev/null || true

# --- 2. a leaked temporary directory ----------------------------------------------------
STRAY="${TMPDIR:-/tmp}/umbral-shellinteg-selftest-$$"
if ./scripts/test_hygiene.sh bash -c "mkdir -p '$STRAY'" >/dev/null 2>&1; then
  fail "a leaked temporary directory was NOT caught"
fi
echo "── leaked temporary directory caught"
rm -rf "$STRAY"

# --- 3. a write to the real database ----------------------------------------------------
# The one that mattered most: it is the check that would have caught a test suite writing
# 2490 sessions into the developer's own data.
if ./scripts/test_hygiene.sh bash -c "echo written >> '$UMBRAL_HYGIENE_DB'" >/dev/null 2>&1; then
  fail "a write to the database was NOT caught"
fi
echo "── write to the database caught"

# --- 4. the advisory path -----------------------------------------------------------------
# A developer with a daemon already running must not be failed for a write that daemon may
# own. The check has to stay usable locally or it will be run with `|| true`.
"$WORK/$PROBE_NAME" 30 &
sleep 0.2
if ! ./scripts/test_hygiene.sh bash -c "echo written >> '$UMBRAL_HYGIENE_DB'" >/dev/null 2>&1; then
  fail "a database write was treated as a failure although a daemon was already running"
fi
echo "── database write downgraded to a note when a daemon was already running"
pkill -u "$(id -u)" -f "$PROBE_NAME" 2>/dev/null || true

echo
echo "test:hygiene:selftest: OK — every leak the gate exists to catch turns it red"
