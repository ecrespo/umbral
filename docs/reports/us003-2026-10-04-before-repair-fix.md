# US-003 live run — 2026-10-04, before the repair-message fix

Kept as evidence for `us003-2026-10-04.md`: an earlier revision of the harness, run before
`registry.message` sent the validator's text instead of a printed Go value. Its invalid-call
row counts every invalid call, before repair.

T-F1-21. The reference scenario of PRD §4.1 and §9, run 20 times against a real daemon
and the local model, by `e2e/us003/run.sh` (`cmd/umbrald/us003_live_test.go`, tag `live`).

## Setup

- **Model:** `ollama/gpt-oss:20b` on the local GPU, `num_ctx = 16384`.
- **Router:** `offline = true`, with a remote candidate (`remote/big-model`, `https://remote.invalid/v1`) listed
  first in the `code` and `fast` classes. REQ-LLM-004 must discard it without contacting it.
- **Each run:**
  - a fresh copy of `e2e/us003/fixture` under git;
  - `go test ./...` typed in a new pane, failing;
  - an `auto-edit` thread in the copy, sent `fix it` with that block attached;
  - `run_command` approved once when the line is a single `go test`, anything else asked about denied;
  - the verdict from running `go test -count=1 ./...` again outside the daemon.
- **Success:** `go test ./...` green and `stats_test.go` unchanged. A run that edits the test fails.
- **Offline:** the panes and the agent run with `GOTOOLCHAIN=local` and `GOPROXY=off`.

## Results

| Measure | Value | Target |
|---|---|---|
| Runs green | 15 / 20 (75.0 %) | ≥ 70 % (PRD §4.1), ≥ 14/20 (T-F1-21) |
| Rows in `egress_log` | 0 | 0 (Art. 4, REQ-SEC-002) |
| Invalid tool calls | 19 of 128 (14.8 %) | < 5 % (PRD §4.1) |
| Runs that edited the test | 0 | — |
| Latency, send to turn end | p50 1m18s · p90 2m29s · max 4m50s | — |
| Tokens | 246479 in · 16733 out | — |

Verdict: T-F1-21's Done is **met**.

## Runs

| # | Green | Stop | Steps | Invalid | Time | Tools | Approved | Denied | Note |
|---|---|---|---|---|---|---|---|---|---|
| 1 | yes | `end_turn` | 6 | 1 | 1m39s | list_dir list_dir read_file read_file edit_file read_file | — | — |  |
| 2 | yes | `end_turn` | 9 | 1 | 2m2s | list_dir list_dir read_file read_file edit_file read_file edit_file read_file run_command | `go test ./...` | — |  |
| 3 | yes | `end_turn` | 7 | 0 | 2m11s | run_command list_dir read_file read_file read_file edit_file run_command | `go test ./...`, `go test ./...` | — |  |
| 4 | yes | `end_turn` | 10 | 0 | 2m24s | run_command glob read_file read_file edit_file read_file edit_file read_file edit_file run_command | `go test ./...` | `run_command: ls -R` |  |
| 5 | yes | `end_turn` | 6 | 1 | 59s | list_dir list_dir read_file edit_file read_file run_command | `go test ./...` | — |  |
| 6 | no | `end_turn` | 18 | 2 | 4m50s | read_file list_dir read_file read_file read_file edit_file read_file edit_file read_file edit_file read_file edit_file read_file edit_file edit_file read_file edit_file read_file | — | — | go test still fails: stats_test.go:33: Median([4 1 3 2]) = 3, want 2.5 |
| 7 | yes | `end_turn` | 6 | 0 | 2m23s | run_command glob read_file read_file edit_file read_file | — | `run_command: ls -R` |  |
| 8 | yes | `end_turn` | 9 | 2 | 1m19s | list_dir run_command list_dir read_file edit_file run_command read_file edit_file run_command | `go test ./...`, `go test ./...`, `go test ./...` | — |  |
| 9 | no | `provider_error` | 0 | 0 | 6s |  | — | — | go test still fails: stats_test.go:33: Median([4 1 3 2]) = 3, want 2.5 |
| 10 | yes | `end_turn` | 7 | 0 | 1m14s | glob read_file read_file edit_file read_file read_file run_command | `go test ./...` | — |  |
| 11 | yes | `end_turn` | 6 | 1 | 2m29s | list_dir list_dir read_file read_file edit_file read_file | — | — |  |
| 12 | no | `tool_error` | 2 | 2 | 9s | list_dir list_dir | — | — | go test still fails: stats_test.go:33: Median([4 1 3 2]) = 3, want 2.5 |
| 13 | no | `tool_error` | 4 | 3 | 19s | read_file list_dir read_file write_file | — | — | go test still fails: stats_test.go:33: Median([4 1 3 2]) = 3, want 2.5 |
| 14 | no | `end_turn` | 3 | 1 | 26s | list_dir list_dir read_file | — | — | go test still fails: stats_test.go:33: Median([4 1 3 2]) = 3, want 2.5 |
| 15 | yes | `end_turn` | 7 | 3 | 2m16s | list_dir list_dir open_file read_file open_file read_file edit_file | — | — |  |
| 16 | yes | `end_turn` | 5 | 0 | 1m7s | glob read_file read_file edit_file run_command | `go test ./...` | — |  |
| 17 | yes | `end_turn` | 5 | 0 | 1m18s | glob read_file read_file edit_file read_file | — | — |  |
| 18 | yes | `end_turn` | 6 | 1 | 1m4s | list_dir list_dir read_file read_file edit_file read_file | — | — |  |
| 19 | yes | `end_turn` | 6 | 0 | 1m4s | run_command list_dir read_file read_file edit_file run_command | `go test ./...`, `go test ./...` | — |  |
| 20 | yes | `end_turn` | 6 | 1 | 42s | list_dir glob run_command read_file edit_file run_command | `go test ./...`, `go test ./...` | — |  |

## Invalid tool calls

- run 1: `list_dir {"path":"","depth":2} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 2: `list_dir {"path":"","depth":3} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 5: `list_dir {"path":"./repo","depth":2} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 6: `read_file {"path":"stats.go","line_start":1,"line_end":400} → invalid call: invalid tool input: read_file: /: &{[line_start line_end]}. Repair the arguments so they match the input schema of read_file — a JSON object wit…`
- run 6: `read_file {"path":"stats.go","line_start":1,"line_end":400} → invalid call: invalid tool input: read_file: /: &{[line_start line_end]}. Repair the arguments so they match the input schema of read_file — a JSON object wit…`
- run 8: `list_dir {"path":"","pattern":""} → invalid call: invalid tool input: list_dir: /: &{[pattern]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every requir…`
- run 8: `list_dir {"path":"","pattern":""} → invalid call: invalid tool input: list_dir: /: &{[pattern]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every requir…`
- run 11: `list_dir {"path":"","depth":3} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 12: `list_dir {"path":"","depth":2} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 12: `list_dir {"path":"","depth":2} → invalid call: invalid tool input: list_dir: /: &{[depth]}`
- run 13: `read_file {"path":"stats_test.go","line_start":1,"line_end":400} → invalid call: invalid tool input: read_file: /: &{[line_start line_end]}. Repair the arguments so they match the input schema of read_file — a JSON object wit…`
- run 13: `read_file {"path":"stats.go","line_start":1,"line_end":400} → invalid call: invalid tool input: read_file: /: &{[line_start line_end]}. Repair the arguments so they match the input schema of read_file — a JSON object wit…`
- run 13: `write_file {"path":"stats.go","offset":1,"limit":400} → invalid call: invalid tool input: write_file: /: &{[content]}; /: &{[offset limit]}`
- run 14: `list_dir {"path":"","depth":3} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 15: `list_dir {"path":"","depth":2} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 15: `open_file {"path":"stats.go","line_start":1,"line_end":400} → invalid call: unknown tool: open_file. There is no tool named open_file; call one of the tools you were offered, by its exact name, with arguments that match it…`
- run 15: `open_file {"path":"stats_test.go"} → invalid call: unknown tool: open_file. There is no tool named open_file; call one of the tools you were offered, by its exact name, with arguments that match it…`
- run 18: `list_dir {"path":"","depth":2} → invalid call: invalid tool input: list_dir: /: &{[depth]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every required…`
- run 20: `list_dir {"path":"","pattern":"."} → invalid call: invalid tool input: list_dir: /: &{[pattern]}. Repair the arguments so they match the input schema of list_dir — a JSON object with every requir…`
