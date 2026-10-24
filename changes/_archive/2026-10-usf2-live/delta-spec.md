# Delta — US-F2-001 measured live

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-24` by the implementing agent under the Tech Lead's standing F2 instruction; folded into PRD F2 0.7 §9 and §10 (Q-F2-01). Open questions 1 and 2 go to the Tech Lead (T-HRD-02); question 3 is for the F2a close |
| **Date** | 2026-10-24 |
| **Task** | T-F2-12 |
| **REQ** | REQ-ORC-001, REQ-ACP-002, REQ-ACP-007; NFR-F2-01, NFR-F2-02, NFR-F2-03 |

## What the measurement found that the PRD says otherwise

PRD F2 §9 says that, since OpenCode is pointed at the local Ollama, "the whole scenario runs
offline". The live run (`docs/reports/usf2-2026-10-24.md`) showed that this is not true.

OpenCode 1.18.33 is launched in the isolated home REQ-ACP-009 gives it, and that home starts with
an empty cache. In its first 80 seconds the agent did two things:
- it installed the `@ai-sdk/openai-compatible` provider package from `registry.npmjs.org`;
- it fetched a 5.9 MB model catalogue.

After that its connections were local only.

This is the agent's own traffic. Art. 4's amendment and REQ-ACP-007 leave it unobserved and
require it to be disclosed. Umbral recorded none of it and the thread reads `unobserved`, so no
requirement is broken. Only the scenario's description is wrong.

## Decisions

1. **PRD F2 §9 says what was measured.** The sentence becomes: the ACP agent's model calls go to
   the same local Ollama. Anything else its own process sends stays its own unobserved traffic.
   On OpenCode 1.18.33 that includes, at start, the provider package from npm and the model
   catalogue. The thread reads `egress_visibility = unobserved`. Q-F2-01 in §10, which gave the
   same reason ("so US-F2-001 runs offline"), now says the agent's model calls stay local and
   points at §9. The decision itself, OpenCode as the reference agent, is unchanged.
2. **What T-F2-12 measures.** The scenario is run as written. The report records:
   - each criterion of T-F2-12's Done per run;
   - the three budgets;
   - every run, including those where a model did not do its task.

   Two things are asserted of the daemon:
   - **Isolation, merges and traces:** each diff stays inside its task's files, the main copy is
     untouched before the merges, and every turn has its trace.
   - **Mediation:** every non-read row of the hosted thread is `allow`, `deny` or `ask`. The one
     exception is the agent's report that the model named a tool the agent does not have, which
     runs nothing. Independently of any label, every file the hosted thread changed was written by
     Umbral on an allowed `fs/write_text_file`.

   The quality of each agent's work is logged, not asserted.

   The three F2a budgets are asserted by tests under the `live` tag, run by `e2e/usf2/run.sh`, and
   are not `task perf` gates: the 1 GiB repository of NFR-F2-01 takes about 20 s to build on every
   run. PRD §7 keeps the F1 budgets' gates and does not ask for gates on these. Whether they should
   become CI gates is the Tech Lead's call, raised with question 3.

   The test also confines what runs during the scenario, because the simulated user approves every
   command: the daemon gets a home of its own, `GOTOOLCHAIN=local` and `GOPROXY=off`, as US-003
   does. The test asserts that `egress_log` stays empty for Umbral's own threads, and that no
   process of the run outlives the daemon.

   This delta does **not** decide what "US-F2-001 passes" (PRD §8) means. That is open question 3.

## Open questions, for the Tech Lead (added to T-HRD-02)

1. **A hosted agent that is offline.** The OpenCode manifest could set the agent's own switches:
   - `OPENCODE_DISABLE_MODELS_FETCH`;
   - `OPENCODE_DISABLE_AUTOUPDATE`;
   - `OPENCODE_DISABLE_SHARE`;
   - `OPENCODE_DISABLE_LSP_DOWNLOAD`;
   - `OPENCODE_DISABLE_DEFAULT_PLUGINS`.

   The provider package would also have to be reachable without npm, for example from a cache the
   user provisions once. Should Umbral do this, and should it be a manifest field ("offline env")
   rather than OpenCode-specific code?
2. **Reads across worktrees.** At start, OpenCode's log lists the main working copy and every
   sibling thread's worktree, which it learns from git. Reads are unmediated by design, so a hosted
   agent can read another thread's worktree. Writes are bounded by the write root. Should the
   manifest switch off what the agent learns about other worktrees, or should REQ-ACP-007's
   disclosure name this?

3. **What "US-F2-001 passes" means.** PRD §8 closes F2a when US-F2-001 passes, but gives no
   pass rate; US-F2-002 has "≥ 70 % over 10 runs". With `gpt-oss:20b` the daemon's properties held
   in every run, while the models did their tasks within their files in one run of four. There are
   three readings:
   - §4's rule, nothing changed outside each worktree, which runs 1 and 3 met;
   - §9's stricter rule, only each task's files, which the test asserts and only run 1 met;
   - k of n runs.

   Which one closes F2a is the Tech Lead's call. *Answered by delta `2026-10-f2a-close`
   (2026-10-24): the daemon's properties in every run, and at least one run completing every task
   within its files.*

## Tests

- `TestLiveUSF2001_REQ_ORC_001`;
- `TestLiveWorktreeCreateOn1GiB_NFR_F2_01`;
- `TestLiveIdleThreadOverhead_NFR_F2_02`;
- `TestLiveACPMediationLatency_NFR_F2_03`.

All four are in `cmd/umbrald`, tag `live`, and run by `e2e/usf2/run.sh`.
