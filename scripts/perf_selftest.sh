#!/usr/bin/env bash
# Proves that the performance gates have teeth: it injects an artificial regression into
# each budgeted benchmark, asserts the benchmark turns red *for the budget*, and removes
# the injection again (T-F0-13's done criterion).
#
# It is the sibling of scripts/arch_selftest.sh and exists for the same reason. A budget
# check that stopped checking — a dropped b.Errorf, a threshold typed in seconds instead of
# milliseconds, a p95 computed off the wrong index — keeps passing, and nothing else in the
# suite would ever notice. scripts/perf_gates.sh proves the green half; this proves the red.
#
# The injection is a sleep inside the timed region, switched on by UMBRAL_PERF_PROBE_DELAY,
# which only the _test.go files read. Each delay is sized against the budget it has to
# break: 10 ms is a regression to a 5 ms budget and noise to a 300 ms one, so the two
# cannot share a number.
#
# Not covered here: REQ-BLK-006's `block.search` budget, which perf_gates.sh runs but which
# has no injection point. It is gated, not self-tested.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

# name | package | benchtime | injected delay
#
# Sample counts are small on purpose: the run has to be slow enough to break the budget
# and quick enough that nobody is tempted to skip it.
PROBES=(
  "BenchmarkOutputLatency_REQ_TERM_006|./internal/api/|100x|10ms|REQ-TERM-006"
  "BenchmarkSessionCreate_REQ_TERM_001|./internal/sessions/integration/|20x|400ms|REQ-TERM-001"
)

failed=0
for probe in "${PROBES[@]}"; do
  IFS='|' read -r name pkg benchtime delay req <<< "$probe"
  echo "── injecting $delay into $name"

  output="$(UMBRAL_PERF_PROBE_DELAY="$delay" \
    go test -run '^$' -bench "^${name}\$" -benchtime "$benchtime" "$pkg" 2>&1)"
  status=$?

  if [ $status -eq 0 ]; then
    echo "$output"
    # Zero exit has two causes and they call for opposite fixes. Either the benchmark ran
    # and its budget let a $delay regression through, or it never ran at all — it skipped,
    # or the name here no longer matches anything. Naming the wrong one sends the next
    # reader to the wrong file, so the result line decides which it was.
    if grep -qE "^${name}(-[0-9]+)?[[:space:]]" <<< "$output"; then
      echo "perf:selftest: FAILED — $name accepted a $delay regression; its budget check is not checking" >&2
    else
      echo "perf:selftest: FAILED — $name never ran; it skipped, or nothing matches that name" >&2
    fi
    failed=1
    continue
  fi

  # A non-zero exit is not enough on its own: a package that stopped compiling, or a
  # missing bash, also exits non-zero and would let a dead gate pass as a live one. The
  # failure has to name the requirement whose budget was missed.
  if ! grep -q "want under.*$req" <<< "$output"; then
    echo "$output"
    echo "perf:selftest: FAILED — $name failed for some other reason than the $req budget" >&2
    failed=1
    continue
  fi

  grep -E "want under|p95" <<< "$output" | sed 's/^/   /'
done

if [ $failed -ne 0 ]; then
  exit 1
fi

echo
echo "perf:selftest: OK — every injected regression was caught by the budget it broke"
