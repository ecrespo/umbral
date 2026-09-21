# Delta — what a restart restores, what it must not run, and where the settings live

| Field | Value |
|---|---|
| **Status** | `PENDING APPROVAL — applied to specs/ on 2026-09-20, awaiting ratification` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-18 |
| **Raised by** | T-F0-18: Data Model §6 step 5 and REQ-TERM-011 say opposite things about the same command, and four things the task needs are specified nowhere |

## Evidence

**1. Two MUSTs contradict each other, and one of them is a safety rule.**

Data Model §6 step 5: "each pane launches a fresh shell, **or its `command_json` when it has
one** (REQ-TERM-009)."

REQ-TERM-011: "IF a restored pane has a stored launch command, THEN THE SYSTEM SHALL leave it
visible in the pane **without running it**, and SHALL run it only after the user confirms, **so
that a restart never re-executes commands on its own**."

These cannot both hold. REQ-TERM-009 itself says "launch a **fresh shell** in each restored
pane" and does not mention the command, so §6's clause is the outlier — and the requirement it
contradicts carries its own rationale. The danger is concrete: a pane whose command was
`terraform apply`, `make deploy` or `rm -rf build` re-runs on every daemon start, unattended,
possibly after a crash the command caused.

**The implementation currently follows §6.** `layout.apply` launches stored commands today
(T-F0-15), which REQ-TERM-011's last sentence forbids in as many words: "`layout.apply`
behaves the same way: it returns the commands as pending, never as launched."

**2. "Pending" has no representation.** REQ-TERM-011 says `layout.apply` *returns* the commands
as pending, so pending is a state a client can see — and nothing in §4's `Pane`, §5.8's
response or the data model can express it. Nor is there anywhere to record that a pane's
command has not been run yet, so the state would be lost at the next restart, which is the one
event it exists to survive.

**3. `pane_history` is in the wrong migration.** Data Model §2.4d puts it in migration `0004`,
the agent subdomain, which T-F1-01 writes. REQ-TERM-010 is an F0 MUST and T-F0-18 is an F0
task: it cannot wait for F1 to create its table. This is the same collision the
`2026-09-structure-migration` delta resolved for the structure tables, and the resolution is
the same shape.

**4. The settings file is named and never specified.** REQ-TERM-010 and Data Model §2.4d both
write `[experimental] pane_history = true`, which is TOML section notation — but no artifact
says where the file is, what format it has, what happens when it is absent or malformed, or
which other settings it holds. `internal/config` today holds path resolution and the instance
lock and reads no file at all.

**5. Focus is not persisted and has nowhere to go.** API §5.3 and §5.7 both answer "the focused
tab"; the service keeps it in memory and loses it on restart, so a restored client opens on
whichever workspace sorts first rather than the one in use. `workspaces` and `tabs` have no
column for it. T-F0-15 and T-F0-16 both recorded this as belonging to T-F0-18.

## Decision

**1. A restart never runs a stored command.** Data Model §6 step 5 drops "or its `command_json`
when it has one". Every restored pane launches a **fresh shell**, whatever it was running
before. A pane that had a command keeps it, marked pending.

**2. Pending is a state, it is on the wire, and it is persisted.**

- `panes.command_pending` (INTEGER 0/1) records that a pane's stored command has not been run.
- §4's `Pane` gains `command_pending`, omitted when false, so a client reads its absence as
  "nothing waiting" exactly as it reads an absent `command` as "a shell".
- `layout.apply` sets it on every pane it creates with a command, and says so in its response;
  restore sets it on every pane that has one.
- `pane.split` does **not** set it. The distinction is who asked and when: a client calling
  `pane.split` with a command is asking for that command to run, now, in the foreground of the
  user's attention. A restart and a `layout.apply` replay an intention from another time, and
  possibly another machine.

**3. "Visible in the pane" means visible in the pane.** THE SYSTEM SHALL write the pending
command into the restored pane's shell input **without a trailing newline**, once that shell
reports its first prompt, so it sits at the prompt exactly as if the user had typed it. The
confirmation REQ-TERM-011 requires is then the user pressing Enter, which needs no method, no
dialog and no new client code — and a user who does not want it presses Ctrl-C or edits it
first, which is the same gesture they already know.

The daemon waits for the prompt because typing into a shell that has not drawn one loses the
input; a pane whose shell has no integration (§5.11, REQ-BLK-003) is given a short grace period
and then typed into anyway, because a visible command that arrived early is a better failure
than a command that never appears. `command_pending` stays true either way: it records that
*Umbral* did not run it, and the daemon does not watch the user's keystrokes to find out
whether they did.

**4. `pane_history` moves to migration `0004_restore`, and the agent subdomain becomes
`0005_agent`.** Migrations are forward-only (Art. 6) and neither `0004` nor `0005` is written,
so this is a renumbering of unwritten files. `0004_restore` carries `pane_history`,
`panes.command_pending` and the two focus columns of decision 6 — everything a restart needs
and nothing else.

**5. The settings file is `$XDG_CONFIG_HOME/umbral/config.toml`**, TOML, read once at start.
An absent file is the default configuration and is not an error: Umbral runs with no
configuration at all, which is what a local-first tool owes a new user. A malformed file **is**
an error and the daemon refuses to start, because the alternative — falling back to defaults
after the user asked for something — is how `pane_history = true` silently becomes false and a
user believes their screens are being captured when they are not. The same reasoning as
REQ-SEC-010's "SHALL NOT fall back to an empty rule set", pointed the other way.

For F0 the file holds one section and one key:

```toml
[experimental]
pane_history = false
```

An unknown key is a warning, not an error, so a configuration written for a later version
still starts this one.

**6. Focus is persisted on the workspace.** `workspaces.focused_tab_id` names the tab focused
within that workspace, and `workspaces.focused_at` is when the workspace itself was last
focused; the focused workspace is the one with the greatest `focused_at`. No singleton row and
no second table: the state is already per workspace, and a `focused_at` restores the same
answer the service computed in memory.

## MODIFIED

### specs/data-model/umbral-schema.md → §6 step 5
- **Before:** "each pane launches a fresh shell, or its `command_json` when it has one".
- **After:** every pane launches a fresh shell; a stored command is marked pending and typed at
  the prompt, never executed (REQ-TERM-011).

### specs/data-model/umbral-schema.md → §2.4b `panes`, §2.4d `pane_history`, §5
- **After:** `panes.command_pending`; `workspaces.focused_tab_id` and `workspaces.focused_at`;
  `pane_history` moves to `0004_restore` and the agent subdomain to `0005_agent`; §5's
  per-migration table list follows.

### specs/api/umbral-daemon-api-v1.md → §4 `Pane`, §5.8 `layout.apply`
- **After:** `command_pending`, omitted when false; `layout.apply` states that the commands it
  reproduces are pending rather than launched, and its warning says so.

### specs/technical/umbral-architecture.md → §5.1 and the configuration section
- **After:** `$XDG_CONFIG_HOME/umbral/config.toml`, read once at start, absent is default,
  malformed refuses to start, unknown keys warn.

### specs/prd/umbral-mvp.md → REQ-TERM-010
- **After:** names the file that carries the setting. The criterion is unchanged.

## NOT MODIFIED

`pane.split` keeps launching a command it is given: REQ-TERM-011 governs restored panes and
`layout.apply`, and §5.6 is unchanged. `protocol_version` stays at 1 — `command_pending` is an
added optional member a client may ignore.

## Verification

- `TestRestoreRebuildsStructure_REQ_TERM_009` — a daemon restarted over a populated database
  comes back with the same workspaces, tabs and panes, each with a fresh session, and the
  previous run's sessions marked `exited`.
- `TestRestoreNeverRunsStoredCommand_REQ_TERM_011` — a pane whose command writes a sentinel
  file is restored, the file does not appear, and the command is on the pane's screen.
- `TestApplyReturnsCommandsAsPending_REQ_TERM_011` — `layout.apply` no longer launches.
- `TestPaneHistoryDisabledByDefault_REQ_TERM_010` — with no config file nothing is captured and
  nothing is replayed; with the setting on, a restored pane's screen is replayed before the new
  shell's output.
- `TestMalformedConfigRefusesToStart` — a settings file the daemon cannot parse stops it,
  rather than being silently replaced by defaults.
- `TestRestoreKeepsFocus_REQ_TERM_009` — the focused workspace and tab survive the restart.
