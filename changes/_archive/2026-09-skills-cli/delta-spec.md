# Delta — skills: instruction bundles the user installs with `umb`, and Umbral's agent loads on demand

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — approved by E. Crespo together with the other three F1 deltas; the frame-limit reading "the CLI raises the limit" and the untainted catalog descriptions were chosen explicitly. Folded into PRD 1.13, Data Model 1.9, Plan 1.10 and the F1 tasks file; the API and Tech Design text is carried by each task's Spec edits and Files, and both documents say it is pending` |
| **Date** | 2026-09-26 |
| **Tasks** | T-F1-34, T-F1-35, T-F1-36 |
| **Raised by** | The Tech Lead, 2026-09-26: "el feature de la posibilidad de instalar skills en umbral usando el cli"; scoped the same day to **Umbral's own agent**, with parity with MCP ("es para el agente de umbral. Sí, añádelos") |

## Evidence

**The agent's only reusable instructions are the rules files.** REQ-CTX-001 puts `AGENTS.md`,
`CLAUDE.md`, `WARP.md` and `CRUSH.md`, found from the repo root down to the cwd, into every turn.
That works for "how this repository works". It fails for "how to do *this kind of task*":

- a release checklist;
- how to read one particular log format;
- the steps of a runbook.

Those are not per repository, and they cost tokens on every turn when only a few turns need them.
Other agentic tools answer this with **skills**: a small directory with a `SKILL.md` whose front
matter gives a name and a one-line description. The agent always sees the descriptions and reads
a body only when it decides the skill applies. Umbral has none of this: no requirement in the PRD,
no method in the API, no table in the data model, no command in `umb`. The word "skill" does not
occur in `specs/`.

**Not the plugins PRD §5.2 defers to F2.** A plugin with a manifest loads *code* into the product.
A skill loads nothing: its files are text that the agent may read, and anything that executes
still goes through `run_command` and the policy engine (decision 5). This is why skills fit F1
without reopening §5.2. §5.1 gains the line "skills for Umbral's agent: text bundles, installed
locally".

**External agents are out of scope.** Agents running *inside* a pane, such as Claude Code or
Codex, have their own skills and do not see Umbral's. Exposing Umbral's skills to them would make
Umbral an MCP server, and it is only a client. That is another feature, outside the MVP.

## Decisions

**1. A skill is a directory with a `SKILL.md`.**

- **Front matter** (YAML):
  - `name`: 1–64 characters of `[a-z0-9-]`, not starting with `-`, unique;
  - `description`: 1–1024 characters, what the agent reads to decide whether the skill applies;
  - `version`: optional, free text up to 64 characters.
- **Body:** Markdown instructions.
- **Other files** may sit beside it, such as reference text or scripts. They are read only on
  demand.
- **Bundle checks.** A bundle that breaks any of them is refused whole, with `VALIDATION_ERROR`
  naming the entry and the rule:
  - only regular files and directories: no symlinks, hardlinks, devices, FIFOs or sockets;
  - no absolute path and no `..` component;
  - modes normalised on copy: `0644` for files, `0755` for directories, and no setuid, setgid or
    sticky bits. Exec bits are dropped, because a script is text until `run_command` runs it
    through an interpreter the agent names;
  - at most 1 MiB per file, 8 MiB per bundle and 256 entries. For a `.tar.gz`, the limits are
    enforced **while streaming**, on decompressed bytes, so a compression bomb stops at the
    first byte over the limit instead of after it has filled the disk.
- **The digest.** The SHA-256 is taken over a canonical manifest: the entries sorted by path,
  each written as `path NUL size NUL sha256(content) LF`, then hashed. The same bundle always
  gives the same digest, whether it came from a directory or a tarball.

**2. The daemon owns installed skills, and `umb` only asks (DD-001).**

- **Where they live.** A bundle is stored in `$XDG_DATA_HOME/umbral/skills/<name>/`.
- **The record** has the shape of `mcp_servers` (Data Model §2.13): an `id` with a new `skl_`
  prefix, a ULID from `store.NewID`, plus `name UNIQUE`. The API and `umb` address skills by name,
  exactly as they address MCP servers. Art. 6 needs no amendment.
- **Atomic writes.**
  - **Install** extracts into `skills/.staging-<id>/`, validates, then renames the directory into
    place in the same transaction window as the row.
  - **Replace** renames the old directory aside, renames the new one in, updates the row, then
    deletes the old one.
  - **Remove** deletes the row, then the directory.
- **Recovery.** Data Model §6 gains a step that runs at start: remove every `.staging-*`
  directory, and every `skills/<name>/` that has no row. A crash mid-install leaves nothing that
  the next start does not clean up.
- **The source path is display only.** The installed copy is what is used, so editing or deleting
  the source changes nothing until the next `--replace`.

**3. Inspect, confirm, then install exactly what was confirmed.**

- **`skill.inspect {path}`** validates the bundle and returns its manifest, without copying
  anything: name, description, version, files with sizes, and the digest.
- **`skill.install {path, expected_sha256, replace?}`** re-validates, and refuses with `CONFLICT`
  if the digest no longer matches. What gets installed is therefore exactly what the user saw,
  with no window for the bundle to change between the look and the copy.
- **`umb skill install <path>`** calls `inspect`, prints the manifest, asks on a terminal (or
  requires `--yes` when there is none), then calls `install` with the digest it showed.
- **Nothing in a skill is executed at any point.** The confirmation is where the user decides to
  trust the instructions: the same trust they give a rules file they commit.

**4. Local paths only, in F1.**

- **Both sides classify the source, before any path is resolved.** `umb` does it before making
  the argument absolute, and the daemon checks again. A source is **not local** if it:
  - contains `://`, which covers any URL scheme;
  - matches scp-like syntax `user@host:path`, that is `^[^/@]+@[^/:]+:`;
  - starts with `git@`.
  Otherwise it is a path. Without this ordering, `https://x` would become `$PWD/https:/x` and
  come back as "not found" instead of being refused under this decision.

- **What is accepted.** A directory, or a `.tar.gz`, on this machine.
- **What is refused.** A URL, a `git` remote or any other network source gets `VALIDATION_ERROR`
  naming this decision. Art. 5 says rule material arriving over the network SHALL be signed and
  verified. Skills are instructions to an agent with a shell, so they are rule material in every
  sense that matters.
- **"Local" is about transport, not about origin.** A tarball the user downloaded is still
  network-origin material. For it, the control is decision 3's inspect-and-confirm, not a
  signature.
- **Signed skill bundles are later work.** They belong beside DD-016's trust store (T-F1-30), in a
  later delta. They are not a hole in this one.

**5. The agent sees descriptions, and reads bodies through a tool. What it reads is untrusted.**

- **The catalog.** It is assembled for every turn, after the rules files and inside the token
  budget of REQ-CTX-004. It lists each enabled skill's name and description, with the description
  cut to 256 characters in the catalog. At most 64 skills may be enabled at once, which bounds
  the catalog at about 21 KiB:
  - enabling a 65th is refused with `VALIDATION_ERROR`;
  - **installing** one while 64 are enabled succeeds, but the skill is stored **disabled**, and
    `umb` says so.
- **Reading a body.** A built-in tool, `skill_load(name, file?)`, returns `SKILL.md` or one named
  file of the bundle as text.
  - Its risk class is **`ReadOnly`** (REQ-AGT-009).
  - `file` is resolved inside the stored bundle only. A path that escapes it, or names a file that
    is not there, is `VALIDATION_ERROR`.
  - Anything over REQ-CTX-005's 256 KiB is truncated the same way.
- **Descriptions do not taint; bodies do.** The catalog is third-party text as well, but it is
  bounded differently:
  - it is at most 256 characters per skill;
  - the user read it at install, because inspect shows it before the confirmation;
  - `umb skill list` shows it at any time.

  Tainting every turn whenever one skill is enabled would make taint mean nothing, because it
  would be on almost always. That exemption is a decision, and it is written into the
  REQ-SEC-006 amendment. The Tech Lead may choose the stricter reading at ratification (see
  "Open for the Tech Lead").
- **Taint.** `skill_load` output is **untrusted content** for REQ-SEC-006: a turn that loaded a
  skill needs approval for `Exec` and `Network`, even where an `allow` rule would otherwise let
  them through. A skill may come from a third party, and its instructions are exactly the
  injection vector taint exists for. The cost is an approval the user would, at worst, have given
  anyway. REQ-SEC-006's list is amended to name it.
- **Scripts are not special.** A script shipped in a skill is text until the agent runs it with
  `run_command`. That is `Exec`, so it goes through `security.Decide()`, and it is tainted as
  above. No skill can widen a policy, pre-approve a tool, or add one to the registry.
- **The agent cannot install skills.** `umb skill install`, `enable` and `remove` join the
  destructive-pattern list of Tech §5.3, so if the agent types them, they always ask and `always`
  is ignored. `umb mcp add` joins it too (delta `2026-09-cli-mcp`). Otherwise an agent could
  persist a prompt injection by installing it.

**6. A change reaches a thread's next turn without restarting it.** Install, replace, enable,
disable and remove are all visible from the next turn of every thread, because the catalog is
built per turn. This is the parity REQ-MCP-002 gives MCP servers, and it now has its own
requirement and test.

**7. The surface.** Skills are listed by `name` ascending: a skill has no `started_at`, and the
list is short enough that a name order is the useful one.

| `umb` | Method | Result |
|---|---|---|
| `umb skill install <path> [--replace] [--yes]` | `skill.inspect {path}`, then `skill.install {path, expected_sha256, replace?}` | the manifest, then the `Skill`; `CONFLICT` for an existing name without `replace`, or a changed digest |
| `umb skill list [--json]` | `skill.list` | page of `Skill` |
| `umb skill show <name> [--json]` | `skill.get {name, include?: "none"\|"body"}` | the `Skill`, and the body when asked |
| `umb skill enable\|disable <name>` | `skill.set_enabled {name, enabled}` | the `Skill` |
| `umb skill remove <name>` | `skill.remove {name}` | `{}` |

The contract details:

- **`Skill` record:** `{id, name, description, version?, source, sha256, size_bytes, enabled,
  installed_at}`.
- **Notification:** `skill.changed` `{name, change: "installed"|"replaced"|"enabled"|"disabled"|"removed"}`.
- **Capability:** `skills`, listed in API §2 and §9.
- **Client kinds:** `cli`, `tui` and `desktop` may call `skill.*`.
- **Paths:** `umb` resolves `path` to an absolute path, as `workspace create` does for `cwd`.
  The daemon reads it as the same user, which is the premise of API §2's runtime directory and
  of REQ-SEC-007.

## Specification changes

- **PRD §5.1** gains the skills line. **§6.16 "Skills (SKL)"** is new:
  - **REQ-SKL-001** · MUST · event — WHEN `umb skill install <path>` runs with a local directory or `.tar.gz` that passes the bundle checks, THE SYSTEM SHALL show its manifest and digest, ask for confirmation on a terminal or require `--yes` otherwise, and install exactly the bundle whose digest was shown, without executing any of its content.
  - **REQ-SKL-002** · MUST · unwanted — IF a bundle breaks a check, its digest changed after inspection, or a skill with its name is installed and `--replace` was not given, THEN THE SYSTEM SHALL refuse it with `VALIDATION_ERROR` or `CONFLICT` naming the reason and leave the store and the stored copies unchanged.
  - **REQ-SKL-003** · MUST · event — WHEN `umb skill list`, `show`, `enable`, `disable` or `remove` runs, THE SYSTEM SHALL invoke the `skill.*` method of the same name and print its result, in JSON with `--json`, with the exit codes of REQ-CLI-004.
  - **REQ-SKL-004** · MUST · event — WHEN a turn's prompt is assembled, THE SYSTEM SHALL include the name and the first 256 characters of the description of every enabled skill, at most 64, and SHALL include a skill's body or files only when the agent calls `skill_load`.
  - **REQ-SKL-005** · MUST · ubiquitous — THE SYSTEM SHALL treat `skill_load` output as untrusted content for REQ-SEC-006, run nothing from a skill except through `run_command` under the policy engine, and let no skill change a policy, an approval or the tool registry.
  - **REQ-SKL-006** · MUST · unwanted — IF the source of `umb skill install` is not a local path, THEN THE SYSTEM SHALL refuse it with `VALIDATION_ERROR`, until signed skill bundles are specified (Art. 5).
  - **REQ-SKL-007** · MUST · event — WHEN a skill is installed, replaced, enabled, disabled or removed while a thread exists, THE SYSTEM SHALL reflect the change from that thread's next turn on, without restarting the thread.
- **PRD REQ-SEC-006**'s list of untrusted content gains `skill_load` output. The amendment
  states that the catalog's capped descriptions are not in that list, and why.
- **API:**

  | Section | Change |
  |---|---|
  | §2 | `skill.*` for `cli`; the `skills` capability |
  | §3 | the `skl_` prefix |
  | §4 | `Skill` |
  | §5 | the six methods |
  | §6 | `skill.changed` |
  | §9 | the capability name |
- **Data Model:**
  - §2.4f `skills`, created by **T-F1-01 in migration 0005**.
  - §6 gains the orphan-sweep step.
  - **Written at ratification, not by a later task.** At ratification, §2.4f is written, §5's
    `0005_agent` row gains `skills`, and T-F1-01's What names the table. T-F1-01 then builds 0005
    with it, and no migration that has been applied is ever edited (Art. 6).

  ```sql
  CREATE TABLE skills (
    id           TEXT PRIMARY KEY CHECK (id LIKE 'skl\_%' ESCAPE '\'),
    name         TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64 AND name GLOB '[a-z0-9]*' AND name NOT GLOB '*[^a-z0-9-]*'),
    description  TEXT NOT NULL CHECK (length(description) BETWEEN 1 AND 1024),
    version      TEXT CHECK (version IS NULL OR length(version) <= 64),
    source       TEXT NOT NULL,              -- the path it was installed from, display only
    sha256       TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL CHECK (size_bytes BETWEEN 1 AND 8388608),
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    installed_at INTEGER NOT NULL
  );
  ```
- **Tech Design:**
  - §3.2: `context` gains the skill store, the catalog and its adapter; `tools` gains `skill_load`.
  - §5.3: the destructive patterns.
  - §9.4: `umb skill`.
- **Architecture (Art. 3).**
  - `.go-arch-lint.yml` gives `api` the rows `context-ports` and `context-domain`, as every module
    wired into `api` gets.
  - `skill_load` reaches the store through a `SkillReader` interface declared in `tools/ports`,
    whose types live in `context/domain`. `context/adapters` satisfies it without importing
    `tools`, and `cmd/umbrald` wires the two together.
  - No other dependency rule changes.
- **Versions** are assigned in the order deltas are ratified. After the two F1-first deltas, this
  is PRD 1.15, API 1.17, Tech 1.15 and Data Model 1.9.

## Verification

| Test | What it proves |
|---|---|
| `TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001` | a bundle whose script would create a file if run is installed; the file never appears, the modes are normalised, and the digest matches the manifest's |
| `TestInstallAsksOrNeedsYes_REQ_SKL_001` | without a terminal and without `--yes`: exit 1, `skill.install` never called |
| `TestABadBundleIsRefusedWhole_REQ_SKL_002` | a symlink, a hardlink, a FIFO, `..`, a 2 MiB file and a tar bomb are each refused with the store unchanged; a changed digest and a duplicate name without `--replace` are `CONFLICT` |
| `TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002` | a leftover `.staging-*` directory and an orphan directory are both gone after the recovery step |
| `TestSkillCommandsMirrorTheMethods_REQ_SKL_003` | the surface table, against a recording daemon |
| `TestPromptCarriesDescriptionsNotBodies_REQ_SKL_004` | a disabled skill is absent; an enabled one contributes its capped description only; `skill_load` returns the body; `file` outside the bundle is refused; a 65th enable is refused, and a 65th install is stored disabled |
| `TestASkillCannotRunOrWidenAnything_REQ_SKL_005` | with an `allow` rule for `Exec`, a turn that loaded a skill still asks, and a turn that only saw the catalog does not; the agent typing `umb skill install` or `umb mcp add` asks and ignores `always` |
| `TestARemoteSourceIsRefused_REQ_SKL_006` | `https://…`, `git@…` and `user@host:path` are refused by `umb` before resolving, and by the daemon when sent directly |
| `TestASkillChangeReachesTheNextTurn_REQ_SKL_007` | installed mid-thread, the skill is in the next turn's catalog; disabled, it is gone from the one after |

## Open for the Tech Lead at ratification

- **Descriptions and taint.** The draft exempts the catalog's capped descriptions from taint
  (decision 5). The stricter alternative taints every turn whenever a skill is enabled. It is
  safer, and it means `Exec` and `Network` always ask while any skill is enabled.

## Phase

**F1**, after the context, budget, tool and approval work it plugs into. Estimate 5.5 days across
three tasks.
