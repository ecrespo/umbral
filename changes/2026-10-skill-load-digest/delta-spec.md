# Delta — `skill_load` checks the stored copy's digest

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-10-24`, awaiting the Tech Lead |
| **Date** | 2026-10-24 |
| **Task** | T-HRD-08 |
| **REQ** | REQ-SKL-005 (no change to its text) |
| **Raised by** | Q3 of delta `2026-10-hrd02-open-questions`, answered (b) and ratified by the Tech Lead on 2026-10-24 |
| **Depends on** | — (the branch is stacked on T-HRD-07's only so the Tech Design's version numbers follow) |

## Evidence

`Service.ReadSkill` opened the requested file through an `os.Root` and bounded it at 1 MiB, but
never compared the stored copy with the row's `sha256`. A copy edited after install — a file
changed, added or removed — was served to the agent as the skill the user had inspected.
`TestSkillLoadRefusesAnEditedStoredCopy_REQ_SKL_005` showed all four edits served, red.

## Decisions

1. **One read, checked, then served.** `ReadSkill` reads the whole stored copy once, with the
   same reader install used for a directory (regular files and directories only, a link in the
   copy refusing the load, a hardlink or a file swapped during the read refused, the per-file,
   per-bundle and entry limits applied). The read holds the store's lock, so a reinstall in
   progress is not mistaken for an edited copy. It compares that read's digest with the one installed and serves the
   entry from that read, so there is no window between the check and the use.
2. **A mismatch or an unreadable copy is `VALIDATION_ERROR`.** The message names the skill and
   tells the user to reinstall it. Every file of the skill is refused, not only a changed one.
   The reader's own error names paths under the data directory, so it is logged, not returned:
   a tool error reaches the model and may reach a remote provider.
3. **What stays:** a file the bundle lacks is still `VALIDATION_ERROR`
   (`2026-09-skills-cli` decision 5); a disabled or unknown skill is still `NOT_FOUND`; the
   1 MiB per-file bound now comes from the bundle reader. A bundle is at most 8 MiB, so each
   load hashes at most 8 MiB. `file: "."` cleans to the bundle's root and is read as `SKILL.md`.
   A stored file with more than one link is refused, as at install; a backup tool that hardlinks
   into the data directory therefore makes its skills refuse to load until reinstalled. This is
   accepted: the store is the daemon's own.
4. **No check at start.** Option (c) of Q3, disabling a mismatching skill at daemon start, is
   not taken: the check on load already covers a change made while the daemon runs.

## Open for the Tech Lead

- REQ-SKL-005's EARS text says nothing about the stored copy's integrity; the property lives in
  Tech §5.3b only, as T-HRD-08's REQ line and this delta leave it. Whether it belongs in a PRD
  criterion is the Tech Lead's call.

## Spec changes

- Tech 1.71 §5.3b: `skill_load` reads the stored copy once, checks its digest and serves the
  entry from that read; a changed or unreadable copy is `VALIDATION_ERROR` naming the skill.

## Verification

- `TestSkillLoadRefusesAnEditedStoredCopy_REQ_SKL_005`: a file edited at the same size,
  `SKILL.md` edited, a file added and a file removed; each refuses `SKILL.md`, the default and
  the other file with `VALIDATION_ERROR` naming the skill and "reinstall". Red first: 4 of 4
  subtests served the changed copy. The same-size edit asserts its own size.
- Mutations: skipping the digest comparison reddens all four subtests; comparing the file count
  instead reddens the two edits.
- `TestSkillLoadServesACopyInstalledFromATarball_REQ_SKL_005`: a copy installed from a tarball
  with a top directory loads.
- `TestPromptCarriesDescriptionsNotBodies_REQ_SKL_004` asks for a missing file, a directory and
  `.` on the intact copy before it swaps links in; the links then refuse the load because the
  copy no longer reads as a bundle. Dropping the `.` rule reddens it.
- Spec-guardian: no CRITICAL or HIGH; its findings on the same-size test, the untested missing
  file path, the lock, the path in the error, `.`, the "no link followed" wording and a tarball
  install are fixed; the hardlink refusal is accepted above and the PRD question left open.
