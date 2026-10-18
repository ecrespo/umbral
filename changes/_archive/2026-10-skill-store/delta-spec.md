# Delta — the skill store: where it lives, what a bundle is read as, and what replace and recovery do

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-18` by the implementing agent under the Tech Lead's standing instruction for F1; **pending the Tech Lead's review**. Folded into API 1.39, Tech Design 1.49, Data Model 1.23 and the F1 tasks file |
| **Date** | 2026-10-18 |
| **Tasks** | T-F1-34 |
| **Builds on** | `2026-09-skills-cli` (ratified 2026-09-26), whose decisions 1–4 and 7 T-F1-34 implements |

## Evidence

`2026-09-skills-cli` fixes the bundle checks, the digest, inspect-then-install, the local-only
rule and the method table. Writing T-F1-34 met eleven questions it does not answer: which package
holds the store, how a tarball's top directory is read, what the digest covers when a bundle has
directories, what `skill.get` answers, what a replace keeps, and how recovery settles a replace cut
in half. Each needed an answer before the API text could be written, and none changes a decision
of the ratified delta.

## Decisions

1. **The store is `internal/context/adapters/skills`.** Tech §3.2 gives `context` the skill
   store; the module has no root package, and the store writes files and SQL, which is adapter
   work. Its pure part — the checks, the front matter, the digest, the source classification — is
   `context/domain/skill.go`. `context/ports` gains the `Skills` port and the `skill.changed` event,
   so it may now import `bus`, as `security/ports` does for `rules.update_rejected`. `api` gains
   the rows `context-ports` and `context-domain`, as decision 7 of `2026-09-skills-cli` said.
2. **The directory is `skills/` beside the database:** `$XDG_DATA_HOME/umbral/skills/` by
   default, and wherever `--db` puts the database otherwise, so a test daemon never touches the
   developer's skills. It is created `0700` and holds a `.umbral-skills` marker; a non-empty
   directory without the marker is someone else's and the daemon refuses to start on it rather
   than sweep it (step 9 removes every directory no row records).
3. **The daemon requires an absolute path,** after classifying it (decision 4 of
   `2026-09-skills-cli`): `umb` resolves it, and the daemon's own working directory means nothing
   to the user. A relative path is `VALIDATION_ERROR`. `.tgz` is accepted beside `.tar.gz`.
4. **A tarball with one top directory is read as that directory.** `tar czf x.tar.gz my-skill/`
   is how most tarballs of a skill are made; when `SKILL.md` is not at the root and every entry
   sits under one directory holding it, that directory is the bundle. The digest is then the same
   as the directory's.
5. **The digest covers files only.** Directories carry no content; an empty directory is not
   installed. Counting directories toward the 256 entries still applies.
6. **The front matter is a subset of YAML** read with the standard library (Art. 3): `key: value`
   lines, values plain, double- or single-quoted, or a `>`/`|` block of indented lines. Keys other
   than `name`, `description` and `version` are ignored, so a skill written for another tool
   installs; a key given twice is refused. Every length is counted in characters.
7. **`skill.get` answers `{skill, body}`,** `body` being SKILL.md after its front matter, `null`
   unless `include: "body"`. A wrapper rather than extra members on `Skill`, so `Skill` stays one
   shape everywhere.
8. **A replace keeps the skill's `id` and whether it is enabled,** so replacing an enabled skill
   while 64 are enabled is not refused, and a disabled one is not switched on. It sets the old copy
   aside as `skills/.old-<name>`, renames the new one in, updates the row and then deletes the old
   copy.
9. **Recovery also settles a replace** (Data Model §6 step 9): for a `.old-<name>` a row records,
   the copy at `<name>` is kept only when its digest is the row's — the update committed — and the
   old copy is removed; otherwise `<name>` is removed and the old copy renamed back, because the
   row still describes it. Any other `.old-*`, every `.staging-*` and every directory no row
   records are removed. Names starting with `.` cannot be skill names, so none of
   them can collide with one. Step 9 runs while the daemon starts, before it serves anyone.
10. **Setting the state a skill already has** answers the `Skill` and announces nothing.
11. **`skill.list` is `{items}`, unpaged,** by name: at most a few hundred rows by the entry and
    enable limits' nature, and `mcp.server.list` has the same shape. **For the Tech Lead:**
    `2026-09-skills-cli`'s method table says `skill.list` returns a page; this decision narrows
    that ratified text and is flagged for review rather than taken as settled.
12. **Read defensively.** Beyond the ratified checks: a backslash in an entry path is refused (it
    is a separator on another OS); a path that is both a file and a directory's parent is refused;
    a tarball's decompressed stream, headers included, is bounded at 8 MiB plus one 4 KiB header
    per entry, so long PAX names cannot stretch the read, and a stream that reaches the bound is
    refused even when the cut falls on a block boundary the tar reader takes as a clean end; each
    file is read with a bound of what the limits still allow, plus one byte; a directory's files
    are opened `O_NOFOLLOW` and checked against the walk's `Lstat`, so a swap for a symlink in
    between is refused, and a file with more than one link is a hardlink.

## Specification changes

- **API 1.39:** §3 `skl_`; §4 `Skill`; §5.37 written with decisions 3–7, 8 and 10; §6
  `skill.changed`; §2's `cli` row gains `skill.*` and the `skills` capability; §9's namespace list.
- **Tech Design 1.49:** §3.2's `context` row names the skill store; §5.2's `context` row says
  its ports name a bus event; the pending note keeps only what T-F1-35…37 write.
- **Data Model 1.23:** §6 step 9 gains decision 9.

## Verification

| Test | What it proves |
|---|---|
| `TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001` | decisions 2, 4, 5: the tarball's digest equals the directory's; modes normalised; nothing executed |
| `TestABadBundleIsRefusedWhole_REQ_SKL_002` | decision 3 (a relative path), 6 (no front matter, a bad name), 8 (replace in place, nothing left aside) |
| `TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002` | decision 9 |
| `TestTheDaemonSweepsSkillsAtStart_REQ_SKL_002` | decisions 2 and 9, wired into the daemon's start |
| `TestTheSixtyFifthSkillIsStoredDisabled_REQ_SKL_004` | decision 10 and the 64-enabled limit |
| `TestSkillsOverTheWire_REQ_SKL_001` | decisions 7 and 11, and the §3 mapping |
| `TestAnInterruptedReplaceKeepsTheRecordedCopy_REQ_SKL_002` | decision 9, both orders of the crash |
| `TestAForeignSkillsDirectoryIsNeverSwept_REQ_SKL_002` | decision 2's marker |
| `TestMoreBadBundles_REQ_SKL_002` | decision 12, and links and devices in a tarball |
| `TestATruncatedStreamIsRefused_REQ_SKL_002` | decision 12's stream bound, cut exactly on a header block |
