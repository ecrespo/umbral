# Delta — the database has an owner too, not only the runtime directory

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — commissioned and approved by E. Crespo ("arregla los 3 pendientes"); drafted, applied and archived by the implementing session` |
| **Date** | 2026-09-26 |
| **Task** | T-F0-24 |
| **Raised by** | The `spec-guardian` review of T-F0-22 (finding 4), 2026-09-26 |

## Evidence

**The lock that makes recovery safe guards the wrong file.** Tech §9.4: before it recovers
the database, a daemon takes an exclusive lock on `umbrald.lock` *in the runtime directory*,
because recovery (Data Model §6) rewrites live rows on the premise that the previous process
is gone. That premise is about the **database**; the lock is about the **socket's
directory**. The two coincide only while both are defaulted. `umbrald` takes `--db` and
`--socket` independently, so two daemons with different runtime directories and the same
`--db` each take their own instance lock, each open the same database, and the second one's
recovery marks the first one's live sessions `exited`, abandons their open blocks and — since
T-F0-22 — settles their pending verdicts, all while those sessions are running.

**Not hypothetical for long.** `--db` is how tests, the CLI round trip and anyone running a
second daemon for a experiment point it at a database; the same database under a second
runtime directory is one mistyped flag away. The damage is silent: the first daemon keeps
serving from memory, and the rows say otherwise.

## Decisions

**1. A second lock, beside the database.** `<database>.lock` next to the database file —
`umbral.db.lock` by default — taken with the same exclusive, non-blocking `flock` as the
instance lock, after the instance lock and before the database is opened. A separate file
rather than a lock on the database itself, because SQLite takes its own POSIX locks on that
file and mixing lock families on one file is platform-dependent.

**2. The instance lock stays, first.** It serialises daemons of one installation, and losing
it is not an error: whoever holds it is serving, so `umbrald` exits 0 and an autostarting
`umb` finds it (Tech §9.4). That behaviour is unchanged.

**3. Losing the database lock is an error.** It can only mean a daemon of *another* runtime
directory is using this database, which is a misconfiguration nobody asked for. `umbrald`
logs which database is taken and exits **75** (`EX_TEMPFAIL`) without opening it — not 0,
because nothing is serving the caller's socket, and not 73, because nothing failed to be
created.

**4. Released by closing, and the file is never removed — for either lock.** The descriptor
closing is what drops it, so a killed daemon leaves nothing that blocks the next one. Removing
the file, as the instance lock used to, is a race: a daemon that had already opened it locks
that inode the moment the holder closes, the removal unlinks it, and a third daemon creates and
locks a fresh one — two owners of one database. Found by the `spec-guardian` review of the
merge; `TestAReleasedLockKeepsItsFile` reproduces it. The lock is keyed on the database's
directory, so a symlinked data directory shares it
(`TestASymlinkedDataDirectorySharesTheLock`); a symlink to the database file from elsewhere
does not, and Tech §9.4 says so.

## Specification changes

- **Tech Design §9.4**, the lock paragraph gains the database lock: its file, its order, exit
  75 when it is held, that no lock file is ever removed, and the symlink limit. Tech 1.12.

No REQ changes: the property is Data Model §6's premise, which the instance lock was already
written to protect; this makes the protection cover the file the premise is about.

## Verification

- `TestOnlyOneDatabaseLockIsGranted` and `TestDatabaseLockIsReleasedForTheNextDaemon`
  (`internal/config`), beside the instance lock's own tests.
- `TestASecondRuntimeCannotRecoverALiveDatabase` (`cmd/umbrald`): a daemon serving a live
  session; a second one, with another runtime directory and the same `--db`, run with
  `--check` — which opens, recovers and exits — must exit 75 and leave the first one's session
  `alive`. Without the lock it exits 0 and marks the session `exited`.

## Phase

**F0**, as `T-F0-24`: it closes a hazard in F0's recovery, which T-F0-22 widened.
