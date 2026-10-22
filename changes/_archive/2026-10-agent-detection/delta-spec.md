# Delta — third-party agent detection

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-22 by the implementing agent under the Tech Lead's standing F2 instruction; folded into API 1.42 (§2, §4 Pane, §5.39, §6, §9) and Tech 1.57 (DD-012, §5.2)` |
| **Date** | 2026-10-22 |
| **Task** | T-F2-10 |
| **REQ** | REQ-XAG-001, REQ-XAG-002, REQ-XAG-003 |

## Decisions the PRD and the task file left open

1. **The manifest's shape.** A TOML file, one agent per file:

   ```toml
   name = "aider"            # lower-case letters, digits and hyphens, at most 32
   display_name = "Aider"    # optional; the name when absent
   state_source = "hooks"    # hooks | acp | none
   [detect]
   executables = ["aider"]   # basenames compared with the basename of argv[0]
   argv = ['^\S*python\S*\s+\S*/aider(\s|$)']   # RE2, against argv joined by single spaces
   ```

   - At least one rule and at most 16; a pattern has 1 to 512 bytes.
   - An unknown key makes the file invalid, so a misspelled rule is reported rather than silently
     matching nothing.
   - A file larger than 64 KiB, or one that is not a regular file once a symlink is followed, is
     invalid. A symlink is followed because dotfile managers make them; a FIFO is refused before
     it is opened, so it cannot block the daemon's start.
   - T-F2-05 extends the same file with ACP's launch section (REQ-ACP-009).
2. **The built-in manifests.**
   - OpenCode declares `acp`.
   - Claude Code, Gemini CLI and Codex declare `hooks`.
   - Goose declares `none`.
   - A Node-hosted agent runs as `node <script>`, so its argv pattern names the package or the
     script after `node`, never a bare word: `vim claude.md` and `git commit -m codex` match
     nothing.
3. **Loading and reporting.**
   - `$XDG_CONFIG_HOME/umbral/agents/*.toml` is read once at start, in lexical order. A later
     file naming the same agent wins, and a local manifest replaces the built-in one of its name.
   - "Reported" means two things: a warning in the daemon's log at start, naming the file and
     why, and the `skipped_manifests` list of every `agent.explain` answer.
   - A missing directory is no local manifests and no error.
   - A reason names what is wrong — an unknown key and its line, a syntax error's line, a field
     of the wrong type, a field out of bounds by its name and position — and never quotes a
     value from the file: a parser's rendering shows the surrounding lines, a regular
     expression's error echoes the pattern, and a value can belong in no log.
4. **What is matched.**
   - The pane's root process — the process its session started — and then the processes under
     it are taken breadth first, in pid order within a level, at most 8 levels below the root and
     256 processes. The root is included because a pane launched with the agent as its command,
     or a shell that ran `exec agent`, has no shell above the agent; a shell matches no manifest.
     The agent the user started therefore wins over anything it starts.
   - The process table is read once per scan, not once per pane.
   - For one process, manifests are tried in name order, an executable rule before an argv rule,
     so the answer never depends on map order.
   - A process's argv is read to match and is never stored, logged or answered: it can carry a
     secret (`--api-key …`). `agent.explain` names the executable's basename and the pid instead.
5. **When.**
   - Every live pane is scanned every 2 seconds, and a pane is scanned afresh when
     `agent.explain` asks about it.
   - The label — the agent's name and declared state source, nothing of the process — lives in
     the daemon's memory, keyed by session. It is not stored: a restart starts
     new shells, and the next scan labels them again. So there is no migration.
   - A change of label is announced as `pane.updated` with the pane, as display metadata is
     (API §6).
6. **Detection decides no state.** `detected_agent` is not an input of DD-012's resolution, and
   no scan announces `pane.state_changed`. The state source a manifest declares is a label for
   the user: how the agent could report, not that it does.
7. **Where it is read.**
   - The process table comes from `/proc` on Linux and from `kern.proc.all` and
     `kern.procargs2` on macOS; no command is run.
   - Elsewhere nothing is listed, so nothing is detected.
8. **`agent.explain`** (API §5.39) answers for a pane:
   - `detected_agent`, the same object `Pane.detected_agent` carries, or `null`;
   - `manifest`: the name and where it was loaded from, `builtin` or the file's path;
   - `rule`: its kind (`executable` or `argv`), the pattern, and the matching process's pid and
     executable basename;
   - `state_authority`: the pane's `attention_state` and `state_source`, as DD-012 resolved them;
   - `skipped_manifests`: every file skipped at start, with its reason.

   It is open to `tui`, `desktop` and `cli`, and served under the `agent_detection` capability.
   `umb agent explain <pane>` prints it, or the result itself with `--json`.

## Folded into

- API 1.42: §2 (`agent.explain` for `tui`, `desktop` and `cli`), §4 (`Pane.detected_agent`),
  §5.39 (`agent.explain`), §6 (`pane.updated` on a label change) and §9 (`agent_detection`).
- Tech 1.57: DD-012 (detection labels and never decides) and §5.2 (the `integrations` rule).
