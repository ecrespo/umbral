# Delta — answers proposed for T-HRD-02's eleven open questions

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-10-24 — awaiting the Tech Lead; every question below is "Tech Lead decides"` |
| **Date** | 2026-10-24 |
| **Task** | `T-HRD-02` (`specs/tasks/umbral-hardening-tasks.md`) |
| **REQ** | REQ-SEC-005, REQ-SKL-005, REQ-CLI-008; REQ-ACP-007 and REQ-ACP-009 for Q10 and Q11 |
| **Raised by** | The implementing agent, at the Tech Lead's request to start the Hardening phase ("Cerremos la F2a y comencemos con el hardening") |

## Why one delta

T-HRD-02 lists eleven questions that four ratified deltas left open:

- three from `2026-10-skill-catalog`;
- four from `2026-10-umb-mcp-panel`;
- two from `2026-10-acp-mediation`;
- two from `2026-10-usf2-live`.

`2026-10-usf2-live` also raised a third question, which delta `2026-10-f2a-close` answered.

This delta puts every question in one place, with:

- what the code does today, read on `develop` at `e0a93d1`;
- the options;
- a recommendation.

The Tech Lead answers each one. A question answered "accept in writing" closes here. A question that needs code becomes its own task and its own delta when the work starts, as T-HRD-02's Done line asks. **Nothing here changes `specs/` or code yet.**

## A fact that changes several answers: the daemon already refuses these methods to an agent

Most of these questions were written before T-F2-05. Since T-F2-05 (delta `2026-10-full-auto`, decision 8, ratified 2026-10-23), the daemon has a `userOnly` flag on methods.

**How a refusal works.** A connection whose peer process runs inside an agent's terminal is refused every `userOnly` method (`internal/api/conn.go` `fromAgent`, `cmd/umbrald/agentpeer.go`).

**How the daemon decides a peer is an agent's.**

- The kernel gives the peer's pid (`SO_PEERCRED` on Linux, `LOCAL_PEERPID` on macOS).
- The peer counts as an agent's when it, or one of its parents, is a thread-owned shell.
- It also counts when a thread-owned shell leads the peer's POSIX session.
- While an agent shell lives, a peer whose pid can no longer be read counts as an agent's too.

**Which methods are `userOnly` today.** All of these are:

- `skill.install`, `skill.set_enabled`, `skill.remove` (`internal/api/skills.go:53-57`);
- `mcp.server.add`, `mcp.server.remove` (`internal/api/mcp.go:56-57`);
- `rules.key.add|remove|rotate`, `rules.rollback`, `rules.reset` (`internal/api/rules.go:24-28`);
- `worktree.merge|discard|close_group`, `approval.respond` and `thread.update`;
- `session.create|input` and the workspace tree's creating methods.

**So the method is refused however the command was spelled.** This holds for `umb skill install x`, for `env -S 'umb skill install x'`, for `umb skill $(echo install) x`, and for a raw JSON-RPC client in the agent's PTY.

**What the patterns still add.** They add an approval before the command runs. The daemon's refusal is the floor, and it does not depend on the normaliser.

**The gap.** A hosted ACP agent's own process tree is not a thread-owned shell. OpenCode is a child of `umbrald`, and so is any command it runs itself rather than through `terminal/create`. Umbral advertises the terminal capability, but nothing forces the agent to use it. `AgentShellPIDs` (`internal/sessions/service.go:340`) does not list them, so `fromAgent` is false for them.

- The agent's environment drops `XDG_RUNTIME_DIR` (`internal/acp/domain/env.go:39` inherits only `PATH` and `LANG`), so `umb` does not find the socket by default.
- A process that knows the path can still connect, because it runs as the same user and can read the token.

See Q12.

## The questions

### Q1 · Command forms the normaliser does not see — `2026-10-skill-catalog`, "Open for the Tech Lead" 1

**Question.** `env -S 'umb skill install x'`, a word built by substitution (`umb skill $(echo install) x`), and a client speaking JSON-RPC to the socket all pass the destructive patterns.

**Evidence.** `simpleCommands` (`internal/security/domain/policy_destructive.go`) splits on separators, strips every quote and backslash, and drops assignments and wrappers.

- `env` declares `-S` as taking an argument, so `env -S umb skill install x` is read with `umb` as `-S`'s value. The program becomes `skill`.
- `$(echo install)` is split at the parenthesis, which leaves `umb skill $` and `echo install`.

For the skill, MCP, rules and worktree commands, the daemon's `userOnly` refusal (above) now covers all three forms. For the rest of the list, such as `rm -rf` and `git push --force`, a tainted turn asks for every `Exec` anyway. An untainted turn in a permissive mode does not.

**Options.**

- **(a) Accept in writing.** The patterns are a floor for untainted turns, and the daemon refuses what matters for persistence.
- **(b) Replace the normaliser with a real shell parser.** `mvdan.cc/sh/v3/syntax` is not in `go.mod` yet. Treat any word the parser cannot resolve statically as destructive, so it asks. This includes a command substitution in program or subcommand position, `eval`, and `env -S`'s string.
- **(c) Keep the normaliser and add an ask rule** for any simple command whose program or first argument contains `$`, `` ` `` or `$(`.

**Recommendation: (b), merged with Q8 into one task.** Q8 needs a real tokenizer anyway. Parsing once serves both questions, and it removes the class of blind spots rather than the three instances. Its fallback, "unparseable or dynamic means ask", is the conservative direction REQ-SEC-005 wants.

**Code:** yes. Size M: a new dependency (Art. 9 check), the normaliser rewritten behind `MatchDestructive`, and the existing pattern tests plus a table of evasions. About 2 days.

**Tech Lead decides.**

### Q2 · `umb rules key add|rotate`, `rollback` and `reset` are not destructive patterns — `2026-10-skill-catalog`, "Open for the Tech Lead" 2

**Evidence.** All five methods are `userOnly` (`internal/api/rules.go:24-28`), so they are refused to an agent's terminal whatever the command line looks like. A pattern would only add an approval prompt in front of a call the daemon refuses anyway.

**Options.**

- **(a) Accept in writing:** closed by the daemon's refusal.
- **(b) Add `umb_rules_change` to the built-in list** as well, as defence in depth for the window where `fromAgent` misjudges a peer.

**Recommendation: (b).** It is a one-line pattern, and the approval reaches the user rather than a bare refusal reaching the model, which is the better experience. The decision text should say that the daemon's refusal is the control.

**Code:** yes. Size XS: one pattern and its test.

**Tech Lead decides.**

### Q3 · `skill_load` serves the stored copy without checking its digest — `2026-10-skill-catalog`, "Open for the Tech Lead" 3

**Evidence.** `Service.ReadSkill` (`internal/context/adapters/skills/skills.go:312`) opens the file through an `os.Root` and enforces the 1 MiB per-file bound. It never compares anything with the row's `sha256`. Only `settleReplace`, at start after an interrupted replace, hashes the copy (`ReadBundle`). A bundle is at most 8 MiB.

**Options.**

- **(a) Accept in writing.** The copy is `0644` in a `0700` directory the daemon owns, and anything able to change it is already the user.
- **(b) Hash the whole bundle on every load and refuse with `VALIDATION_ERROR` on a mismatch.** That is one SHA-256 of at most 8 MiB, a few milliseconds.
- **(c) Hash every enabled skill at daemon start and disable a mismatching one with a log line,** plus (b) on load.

**Recommendation: (b).** It is cheap, local and fail-closed, and it is the only option that catches a change made while the daemon runs. The refusal should name the skill and tell the user to reinstall it.

**Code:** yes. Size S: `ReadSkill` calls `ReadBundle`, plus a test that edits the stored copy.

**Tech Lead decides.**

### Q4 · Catalog descriptions reach the system prompt untainted — `2026-10-umb-mcp-panel`, "Open for the Tech Lead" 1

**Evidence.** Decision 1 of `2026-10-skill-catalog` makes each description one line, strips control and format characters, and cuts it to 256 runes. A preamble says a skill cannot widen what the agent may do. The user saw the description and confirmed the digest at install.

**Options.**

- **(a) Accept the install confirmation as the control.**
- **(b) Put the descriptions in a delimited block** whose preamble names them as third-party text, untrusted, never instructions. The turn stays untainted.
- **(c) Show names only,** with the descriptions served through a tainting tool.

**Recommendation: (b).** (c) makes skills hard to choose, and (a) relies on a reading the user may not have done. (b) costs a few lines in the prompt builder and makes the provenance explicit to the model. Taint stays reserved for content the user never saw.

**Code:** yes. Size S: the `# Skills` section's wrapper and preamble, plus a prompt test.

**Tech Lead decides.**

### Q5 · `umb skill`/`umb mcp` from the agent's PTY are guarded only by patterns — `2026-10-umb-mcp-panel`, "Open for the Tech Lead" 2

**Evidence.** This is no longer true. Since T-F2-05, `skill.install|set_enabled|remove` and `mcp.server.add|remove` are `userOnly`, and the daemon refuses them to a connection inside an agent's terminal. That is exactly the remedy this question proposed, done by peer credentials rather than by a token the PTY does not inherit.

**Options.**

- **(a) Record it as closed by delta `2026-10-full-auto` decision 8.**
- **(b) Also close the hosted-agent gap**, Q12.

**Recommendation: (a), with Q12 taken separately.**

**Code:** no, for this question.

**Tech Lead decides.**

### Q6 · `umb mcp remove` is not a destructive pattern — `2026-10-umb-mcp-panel`, "Open for the Tech Lead" 3

**Evidence.** `mcp.server.remove` is `userOnly` (`internal/api/mcp.go:57`), so an agent's `run_command` can no longer remove the user's servers. The built-in list has `umb_mcp_add` only.

**Options.**

- **(a) Accept in writing:** closed by the refusal.
- **(b) Widen `umb_mcp_add` to `add|remove`,** as defence in depth, like Q2.

**Recommendation: (b), in the same change as Q2.** The reasoning is the same as Q2's.

**Code:** yes. Size XS.

**Tech Lead decides.**

### Q7 · Which `env_refs` a `cli` client may set — `2026-10-umb-mcp-panel`, "Open for the Tech Lead" 4

**Evidence.** A `stdio` server runs its command with its references resolved, so a `keyring:` path that holds a provider credential could be handed to it. `mcp.server.add` is `userOnly`, so the caller is the user, outside any agent terminal.

**Options.**

- **(a) Accept in writing.** The user may hand their own secret to a program they chose.
- **(b) Reserve a namespace:** an MCP server's `env_refs` may name only `keyring:mcp/...` paths or environment variables, and a provider's keyring path is refused with `data.reason`.
- **(c) Confirm interactively,** before the first start of a CLI-added server, which references it receives.

**Recommendation: (a).** Since T-F2-05 the only caller is the user, and (b) would break a legitimate setup, such as an MCP server for the same provider, while protecting the user only from themselves. If the Tech Lead prefers a guard, (c) is the less surprising one.

**Code:** no, for (a). Size S, for (b) or (c).

**Tech Lead decides.**

### Q8 · The normaliser reads no quotes: `env 'A=x y' rm -rf /` — `2026-10-acp-mediation`, "Open" 3

**Evidence.** Stripping the quotes gives `env A=x y rm -rf /`. `unwrap` drops `env` and `A=x`, and the program becomes `y`, so `rm_recursive` never sees `rm`. ACP terminals are not affected, because Umbral builds their words itself (T-F2-08). A `run_command` line the model writes is affected.

**Options.**

- **(a) The real shell parser of Q1 (b).**
- **(b) A minimal quote-aware tokenizer** inside the current normaliser.
- **(c) Accept it while taint covers the injection case.**

**Recommendation: (a), the same task as Q1.** A hand-written tokenizer would be the third attempt at shell grammar in this file. In an untainted turn, a model that writes this line by mistake still runs `rm -rf /` unasked in `auto-edit` or `full-auto`, so (c) is not enough.

**Code:** yes, shared with Q1.

**Tech Lead decides.**

### Q9 · `run_command` in a zsh PTY loses a last line printed without a newline (`PROMPT_SP`) — `2026-10-acp-mediation`, "Open" 4

**Evidence.**

- ACP terminals avoid this by preferring bash (`terminalShell`, `internal/sessions/terminal.go:112`).
- The thread's own PTY runs the user's shell.
- zsh writes its `PROMPT_SP` mark (`%`, spaces, CR) inside the block before the prompt, which erases an unterminated last line in what `run_command` returns. `cat file-without-trailing-newline` loses its last line.
- The zsh bootstrap is a temporary `ZDOTDIR/.zshrc` (`internal/sessions/adapters/shellinteg/bootstrap.go:107`), which runs before the user's own `.zshrc`.

**Options.**

- **(a) For thread-owned sessions only, `unsetopt PROMPT_SP`** from a `precmd` hook the bootstrap installs, so it runs after the user's rc. The user's own panes are untouched.
- **(b) Take the unfinished line from the stream** before the prompt-end mark, as the ACP terminal's `Output` keeps `plain.Current()`.
- **(c) Accept in writing**, because it is the user's shell.

**Recommendation: (a).** It touches only agent-owned PTYs, which the user does not type into. It fixes the cause rather than reconstructing output, and it is a few lines in the bootstrap. (b) would also have to tell the mark apart from real output.

**Code:** yes. Size S: a bootstrap change, plus a live-ish test with zsh in CI (skipped where zsh is absent).

**Tech Lead decides.**

### Q10 · A hosted OpenCode is not offline — `2026-10-usf2-live`, open question 1

**Evidence.**

- The manifest already sets `autoupdate: false`, `share: "disabled"`, no MCP and no plugin (`internal/acp/domain/manifest.go`, `openCode`).
- The live run still saw two fetches at start: the provider package from npm, and the model catalogue.
- An entry's `env_refs` may not set `OPENCODE_*`, because `EnvPrefixes` reserves them. So the switches `OPENCODE_DISABLE_MODELS_FETCH`, `OPENCODE_DISABLE_LSP_DOWNLOAD` and `OPENCODE_DISABLE_DEFAULT_PLUGINS` can only come from the manifest.
- The isolated cache is per thread (`acp/<thread>/cache`), so the npm install repeats for every new thread.

**Options.**

- **(a) A generic manifest field, `FixedEnv`, written last over the environment.** OpenCode's entry sets the three switches. Combine it with a per-manifest, read-only package cache that the user seeds once with a documented command, and point the agent's `npm_config_cache` / offline flag at it.
- **(b) `FixedEnv` with the switches only.** The npm fetch stays, disclosed by `unobserved`.
- **(c) Accept in writing.** A hosted agent is `unobserved` by REQ-ACP-007, and that disclosure is the control.

**Recommendation: (b) now, (a)'s cache as an F2 slice task.** The switches are generic, cheap and remove recurring traffic. The shared cache crosses thread isolation and needs its own design: who writes it, and how a tampered package is detected. The disclosure stays true either way.

**Code:** yes. Size S for (b): the manifest field, `Environment`, and a test that a ref cannot override it.

**Tech Lead decides.**

### Q11 · A hosted agent learns sibling worktrees from git and may read them — `2026-10-usf2-live`, open question 2

**Evidence.**

- A linked worktree's `.git` file points into the main copy's `.git/worktrees/`, and `git worktree list` from inside it lists every sibling. Hiding them would break git for the agent.
- Reads are unmediated by design (REQ-ACP-007's disclosure). Writes are bounded by the write root.

**Options.**

- **(a) Name it in REQ-ACP-007's disclosure:** a hosted agent can read the main copy and every other thread's worktree in the repository.
- **(b) Confine the agent's filesystem with an OS sandbox,** such as Landlock on Linux or `sandbox-exec` on macOS, so it reads only its own worktree and its isolated home.
- **(c) Both:** (a) now, (b) planned.

**Recommendation: (c).** The disclosure is a PRD text change with no code, and it makes the current behaviour honest today. A sandbox is the real fix, but it is platform-specific, and it belongs in an F2 slice with its own delta.

**Code:** no for (a), which is a PRD F2 REQ-ACP-007 wording change. Size L for (b), later.

**Tech Lead decides.**

## Q12 (new, found while answering Q5) · A hosted agent's own processes are not "inside an agent's terminal"

**Evidence.**

- `agentPeer` knows the thread-owned shells only (`AgentShellPIDs`).
- An ACP agent process and the commands it runs itself are children of `umbrald`, outside those shells, so `fromAgent` is false for them.
- With the socket path and the token, which belong to the same user, such a process could call `skill.install`, `mcp.server.add` or `worktree.merge`.
- The allowlisted environment hides the socket's location, but it does not prevent the call.

**Options.**

- **(a) Add each hosted agent's pid to the set `agentPeer` checks.** Its descendants then match through `ancestors`, and its POSIX session through `sid` (`internal/acp` does not start the agent with `Setsid` today, so the session check needs it to).
- **(b) Accept in writing,** relying on the hidden socket path.

**Recommendation: (a).** It is the same rule REQ-ORC-004 already applies, extended to the other kind of agent. The pids are known to `internal/acp` when it launches the agent.

**Code:** yes. Size S: a port from `acp` to `cmd/umbrald`'s `agentPeer`, plus a test with a fake hosted agent calling a `userOnly` method.

**Tech Lead decides.**

## Summary

| # | Source | Recommendation | Code |
|---|---|---|---|
| Q1 | skill-catalog | real shell parser; dynamic words ask (with Q8) | yes, M |
| Q2 | skill-catalog | add `umb_rules_change`; the daemon's refusal is the control | yes, XS |
| Q3 | skill-catalog | hash the bundle on every load, refuse on mismatch | yes, S |
| Q4 | umb-mcp-panel | delimited third-party block in `# Skills` | yes, S |
| Q5 | umb-mcp-panel | closed by `2026-10-full-auto` decision 8 | no |
| Q6 | umb-mcp-panel | widen `umb_mcp_add` to `add\|remove` (with Q2) | yes, XS |
| Q7 | umb-mcp-panel | accept: the only caller is the user | no |
| Q8 | acp-mediation | same task as Q1 | with Q1 |
| Q9 | acp-mediation | `unsetopt PROMPT_SP` in thread-owned zsh only | yes, S |
| Q10 | usf2-live | manifest `FixedEnv` with the switches; cache later | yes, S |
| Q11 | usf2-live | disclose in REQ-ACP-007 now; OS sandbox later | spec only now |
| Q12 | new | hosted agents' pids join `agentPeer` | yes, S |

## If ratified

1. Record each answer in T-HRD-02, with where it was decided.
2. Open the code as Hardening tasks, one per branch:
   - Q1 with Q8;
   - Q2 with Q6;
   - Q3;
   - Q4;
   - Q9;
   - Q10;
   - Q12.
3. Fold Q11 (a) into PRD F2 REQ-ACP-007.
4. Archive this delta.

T-HRD-02 is done when every row names its answer, as its Done line says.
