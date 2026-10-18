# Delta — `umb skill`: the path, the confirmation and the output

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-18` by the implementing agent under the Tech Lead's standing instruction for F1; **pending the Tech Lead's review**. Folded into Tech Design 1.50 §9.4 and the F1 tasks file |
| **Date** | 2026-10-18 |
| **Tasks** | T-F1-35 |
| **Builds on** | `2026-09-skills-cli` (ratified 2026-09-26) decision 3 and its CLI table; `2026-10-skill-store` (T-F1-34) |

## Evidence

`2026-09-skills-cli` gives `umb skill`'s commands and the methods they call, and says install
inspects, prints the manifest, asks on a terminal or requires `--yes`, then installs the digest
it showed. Writing T-F1-35 needed six answers it does not give: who resolves a relative path,
what counts as a confirmation, where the manifest goes under `--json`, how `show` asks for the
body, what `list` prints, and what a skill stored disabled looks like to the user.

## Decisions

1. **`umb` resolves a local path; a remote one is sent as typed.** The daemon requires an
   absolute path (`2026-10-skill-store` decision 3). Resolving `git@host:repo` against the
   working directory would make it look local, so `umb` classifies first with the daemon's own
   `IsRemoteSource` and leaves a remote source for the daemon to refuse (REQ-SKL-006).
2. **The confirmation is a `y` or `yes` read from stdin when stdin is a terminal,** after a
   prompt on stderr naming the skill and its digest. Anything else, end of input included, is a
   no: nothing installed, exit 1. With no terminal and no `--yes`, the manifest is shown, stderr
   says to run again with `--yes`, and `skill.install` is never called.
3. **Under `--json`, stdout holds the method's result alone:** install's manifest moves to
   stderr, and the output of every subcommand is its method's result verbatim.
4. **`umb skill show NAME --body`** sends `include: "body"`; without the flag `include` is not
   sent, so the daemon's default (`none`) applies.
5. **`list` prints one line per skill:** name, version (`-` when there is none), `enabled` or
   `disabled`, and the description's first line cut to 72 characters. The ratified table's "page
   of `Skill`" is `{items}` (`2026-10-skill-store` decision 11, flagged for review there).
6. **An install stored disabled says so,** naming the limit of 64 enabled skills (REQ-SKL-004),
   so the user is not left believing the agent can see it.

7. **Text from the bundle is printed escaped.** The manifest is the only control between a
   downloaded tarball and the store (`2026-09-skills-cli` decision 4), and its name, version,
   description and paths are the bundle's own text. Every rune a terminal would act on rather
   than show — C0 and C1 controls, DEL, newlines, bidi and other format characters — is printed
   as its Go escape in the manifest, `list`, `show` and every confirmation line; `show --body`
   keeps newlines and tabs. **For the Tech Lead:** the daemon still accepts such characters in
   entry names and front-matter values (`2026-10-skill-store`); refusing them there is the
   stronger fix and is left for a delta of its own, since every client, not only `umb`, renders
   these fields.
8. **Each call has its own deadline,** and install's starts after the confirmation: a single
   10 s deadline over inspect, the prompt and install would fail anyone who read the manifest
   for longer than that.

## Specification changes

- **Tech Design 1.50:** §9.4 lists `umb skill` and writes decisions 1–8; the pending note keeps
  only `umb mcp`.

## Verification

| Test | What it proves |
|---|---|
| `TestInstallAsksOrNeedsYes_REQ_SKL_001` | decisions 1, 2, 3 and 8 (an answer typed after three deadlines still installs): no terminal and no `--yes` never installs; a terminal's `n`, empty line, `y` and `yes`; the digest shown is the one installed; a relative path made absolute; a remote source untouched; `--json` prints the `Skill` alone |
| `TestTheManifestCannotHideAFile_REQ_SKL_001` | decision 7: ESC, CR, BEL, newline and bidi override in paths, description and version are printed escaped, and the manifest keeps one line per file |
| `TestOnlyACharacterDeviceIsATerminal_REQ_SKL_001` | decision 2: a pipe, a file and an in-memory reader are never a terminal |
| `TestSkillCommandsMirrorTheMethods_REQ_SKL_003` | decisions 3, 4 and 5: each subcommand's method and params, its output and `--json`, a daemon error exiting 1, bad usage exiting 1 |
