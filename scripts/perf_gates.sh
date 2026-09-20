#!/usr/bin/env bash
# Runs every benchmark that carries an NFR budget and fails if one misses it (T-F0-13).
#
# The budgets are not repeated here. Each benchmark knows its own and reports p95 as a
# metric, because a threshold written twice is a threshold that will disagree with itself;
# this script only decides which benchmarks are gates and how many samples each one gets.
#
# Run by `task bench:gates` and by .github/workflows/perf.yml. `task bench` is the wider
# run — every benchmark, with allocations — and gates nothing.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

# name | package | benchtime
#
# The sample counts are chosen so a p95 means something without the run taking minutes.
# session.create forks a shell per sample and block.search seeds 100,000 rows before its
# first query, so both are counted rather than timed; the output path is cheap enough to
# give a duration and collect tens of thousands of samples.
GATES=(
  "BenchmarkSessionCreate_REQ_TERM_001|./internal/sessions/integration/|200x"
  "BenchmarkOutputLatency_REQ_TERM_006|./internal/api/|2s"
  "BenchmarkBlockSearch100k_REQ_BLK_006|./internal/sessions/adapters/blockstore/|300x"
)

# A plain counter rather than an array: `${#failed[@]}` on an empty array is an unbound
# variable under `set -u` in the bash 3.2 that macOS still ships.
failed=0
names=""

for gate in "${GATES[@]}"; do
  IFS='|' read -r name pkg benchtime <<< "$gate"
  echo "── $name ($pkg, -benchtime $benchtime)"

  # -run '^$' so the ordinary tests do not run again here; `task test` owns those.
  output="$(go test -run '^$' -bench "^${name}\$" -benchtime "$benchtime" "$pkg" 2>&1)"
  status=$?
  echo "$output"

  if [ $status -ne 0 ]; then
    failed=$((failed + 1))
    names="$names $name"
    continue
  fi

  # Zero exit is not enough. A benchmark that skips — BenchmarkSessionCreate does when
  # bash is missing — prints nothing whatsoever without -v: no SKIP line, no result line,
  # just `ok`. So does a name in GATES that matches no benchmark at all, which is what a
  # rename leaves behind. Both are a green tick over an unmeasured requirement, so the
  # result line has to be there.
  if ! grep -qE "^${name}(-[0-9]+)?[[:space:]]" <<< "$output"; then
    echo "perf: $name produced no result line — it was skipped, or nothing matched the name" >&2
    failed=$((failed + 1))
    names="$names $name"
  fi
done

if [ $failed -gt 0 ]; then
  echo >&2
  echo "perf: $failed budget(s) missed or unchecked:$names" >&2
  echo "perf: these are the NFRs in specs/prd/umbral-mvp.md §7; the job is a required" >&2
  echo "perf: check, listed in scripts/github_bootstrap.py --protect-main" >&2
  exit 1
fi

echo
echo "perf: every NFR budget met"
