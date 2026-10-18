# Delta — the skill catalog, `skill_load`, and the destructive patterns

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-18` by the implementing agent under the Tech Lead's standing instruction for F1; **pending the Tech Lead's review**. Folded into Tech Design 1.51 and the F1 tasks file |
| **Date** | 2026-10-18 |
| **Tasks** | T-F1-36 |
| **Builds on** | `2026-09-skills-cli` (ratified 2026-09-26) decisions 5 and 6; `2026-09-cli-mcp`; `2026-10-skill-store` (T-F1-34) |

## Evidence

`2026-09-skills-cli` decision 5 fixes what the agent sees (names and descriptions cut to 256
characters, at most 64), that bodies are read through `skill_load`, that its output taints the
turn, and that `umb skill install|enable|remove` and `umb mcp add` are destructive. Writing
T-F1-36 needed answers on where the catalog sits in the prompt, how the runtime reads it, what
`skill_load` serves and returns, and how the two patterns are written.

## Decisions

1. **The catalog is a `# Skills` section of the system prompt,** after the rules files and
   before the git state: one line `- <name>: <description>` per enabled skill, by name, at most
   64, each description cut to 256 characters (runes, not bytes). A description is made one
   line first — every run of whitespace one space, control and format characters (bidi
   overrides among them) dropped — because a `|` block keeps its newlines and could otherwise
   open a fake `# Git` or close a `</rules>` in the prompt the model reads as Umbral's. A short preamble names
   `skill_load` and says a skill cannot widen what the agent may do. Being in the system prompt,
   it is counted as fixed by REQ-CTX-004's budget.
2. **The runtime reads it for every turn** through a new port, `context/ports.SkillCatalog`,
   which the skill store implements; `agents.Config.Skills` is nil when there is none. Read in
   the turn's preparation, a change reaches the next turn of every thread (REQ-SKL-007).
3. **`skill_load {name, file?}`** is a built-in behind `tools/ports.SkillReader`, which the
   store implements; without a reader it refuses to run. Its target for the policy is `name` or
   `name/file`, so a rule can name a skill.
4. **It serves enabled skills only.** A disabled skill is absent from the catalog, so the agent
   has no reason to load it, and the user disabled it to keep it out of turns. An unknown or
   disabled skill is reported as not found; `2026-09-skills-cli` decision 5 speaks only of
   files, so this adds to it rather than departing from it.
5. **What it returns:** `SKILL.md` whole, front matter included, when `file` is absent; else
   one file of the stored bundle. `file` is checked with the same entry rules as an install (no
   absolute path, `..` or backslash) and opened through an `os.Root` on the skill's directory,
   so no component — a link swapped into the stored copy after install among them — leads out
   of it; it must be a regular file of at most 1 MiB. A file the bundle does not have, or one
   that cannot be opened inside it, is `VALIDATION_ERROR`, as the ratified decision 5 says. The text is cut at 256 KiB with a `[truncated: N bytes omitted]` line, as
   `read_file` cuts. Every result is tainted (REQ-SKL-005); a refusal is a tool error in the
   daemon's own words, so it carries no taint.
6. **The patterns** are `umb_skill_change` — `^umb( \S+)* skill( \S+)* (install|enable|remove)( |$)`
   — and `umb_mcp_add` — `^umb( \S+)* mcp( \S+)* add( |$)` — matched on the normalised simple
   commands like every built-in pattern, so a path to the binary, `sudo`, `env NAME=…`,
   `sh -c`, quoting, chaining and global flags such as `--socket` do not hide them — each of
   those forms is a case of `TestDestructiveAlwaysAsk_REQ_SEC_005`. `disable` and `umb mcp
   remove` are not destructive: they narrow what the agent sees. A rule bundle replaces the
   list, as it replaces every built-in pattern (REQ-SEC-010).
7. **The daemon builds the skill store before the agent runtime,** so the runtime's catalog and
   `skill_load`'s reader are the store the API serves.

## Open for the Tech Lead

- **What the normaliser does not see**, true of every pattern and older than this delta:
  `env -S 'umb skill install x'` (`-S` is read as taking an argument, so it swallows the
  program), a word built by substitution or a variable (`umb skill $(echo install) x`), and a
  client that speaks JSON-RPC to the socket itself. A tainted turn already asks for every
  `Exec`; the patterns add the floor in untainted turns and the refusal of `always`.
- **`umb rules key add|rotate`, `rollback` and `reset`** change policy material too and are
  not on the list; the ratified scope named only the skill and MCP commands.
- **`skill_load` serves the stored copy as it is,** without checking it against the row's
  digest. Install wrote it `0644` in a `0700` directory the daemon owns; a check at every load
  would catch a copy changed behind the daemon's back.

## Specification changes

- **Tech Design 1.51:** §3.2's `tools` row gains `skill_load` and its `context` row the
  catalog; §5.3's built-in destructive list gains decision 6; §5.3b gains `skill_load` and the
  catalog (decisions 1–5); the pending note keeps only §9.4's `umb mcp`.

## Verification

| Test | What it proves |
|---|---|
| `TestPromptCarriesDescriptionsNotBodies_REQ_SKL_004` | decisions 1, 4 and 5: enabled skills by name, descriptions cut at 256 characters, no body and no disabled skill in the prompt; `SKILL.md` and a named file served; a path out of the bundle, a missing file, a directory, a disabled or unknown skill refused, with the error decisions 4 and 5 give; a link swapped into the stored copy not followed |
| `TestTheDaemonListsSkillsAndLoadsThem_REQ_SKL_004` | decisions 2, 3 and 7 on a real daemon: a skill installed over the socket is in the next prompt with `skill_load` offered, and the model's call returns the body |
| `TestTheCatalogIsCappedAndOneLinePerSkill_REQ_SKL_004` | decision 1: 70 enabled skills give 64 entries; a description holding newlines, `# Git`, `</rules>`, ESC and a bidi override is one line of words |
| `TestDestructiveAlwaysAsk_REQ_SEC_005` | decision 6: the two patterns through a path, `sudo`, `env`, `sh -c`, `bash -lc`, quoting, chaining, extra spaces and global flags; `list`, `show`, `disable`, `umb mcp list`/`remove` and `echo umb …` do not match |
| `TestSkillLoadIsReadOnlyAndTainted_REQ_SKL_005` | decisions 3 and 5: ReadOnly, the target, tainted results, the 256 KiB cut, no reader refuses |
| `TestASkillCannotRunOrWidenAnything_REQ_SKL_005` | decisions 5 and 6: with an `allow` rule a catalog-only turn runs without asking, a turn that loaded a skill asks as `tainted`, and `umb skill install`, `enable`, `remove` (behind `--socket`) and `umb mcp add` ask as `destructive` with `always` kept `once` |
| `TestASkillChangeReachesTheNextTurn_REQ_SKL_007` | decision 2: installed mid-thread, in the next prompt; disabled, gone from the one after |
