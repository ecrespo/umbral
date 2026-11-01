# Delta — A hosted OpenCode starts without its own update traffic

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing instruction; pending the Tech Lead's review |
| **Date** | 2026-10-31 |
| **Task** | T-HRD-11 |
| **REQ** | REQ-ACP-009 |
| **Folded into** | PRD F2 0.15 (REQ-ACP-009), Tech 1.88 (§5.3f) |

## Evidence

- T-HRD-02 Q10 (delta `2026-10-hrd02-open-questions`): the live US-F2-001 run (T-F2-12) saw a
  hosted OpenCode fetch from npm and a model catalogue at start, so PRD F2 §9 stopped calling the
  scenario offline. The answer: a manifest field of fixed variables the entry's `env_refs` cannot
  override, with OpenCode's `OPENCODE_DISABLE_*` switches, amending REQ-ACP-009's allowlist.
- The OpenCode binary installed on the development machine (from `~/.opencode/bin`) reads, among
  others, `OPENCODE_DISABLE_AUTOUPDATE`, `OPENCODE_DISABLE_DEFAULT_PLUGINS`,
  `OPENCODE_DISABLE_LSP_DOWNLOAD`, `OPENCODE_DISABLE_MODELS_FETCH` and `OPENCODE_DISABLE_SHARE`.

## Decisions

1. **`Manifest.FixedEnv`.** A manifest may name variables the agent always gets. They are set
   last in the agent's environment, after the entry's resolved refs, and they are reserved like
   the manifest's prefixes: an `env_refs` key naming one stops the daemon at start, and were one
   to reach `Environment`, the fixed value would still win.
2. **OpenCode's fixed variables**, each `1`: `OPENCODE_DISABLE_AUTOUPDATE` (its update check),
   `OPENCODE_DISABLE_DEFAULT_PLUGINS` (its default plugins from npm),
   `OPENCODE_DISABLE_LSP_DOWNLOAD` (language servers), `OPENCODE_DISABLE_MODELS_FETCH` (the
   models.dev catalogue) and `OPENCODE_DISABLE_SHARE` (session sharing, beside `share =
   "disabled"` in the generated file). Only switches that stop traffic the agent starts on its
   own are fixed; the ones that change its behaviour are left alone.
3. **What remains.** The provider package OpenCode installs from npm for a configured provider
   has no `OPENCODE_*` switch (a package manager's own offline or cache setting is Q10's
   option (a), not taken here); it stays the agent's own traffic, as PRD F2 §9 says, and a shared package
   cache stays with a later F2 slice (Q10). The disclosure REQ-ACP-007 requires — Umbral does not
   record the agent's own traffic — stays true and is unchanged.
4. **Not measured live.** The task's criterion is the environment the agent gets; whether a
   given OpenCode release honours each switch is its own behaviour, re-measured whenever
   US-F2-001 runs live.
