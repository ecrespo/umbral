# Delta — Windows: ConPTY, a named pipe, Credential Manager and PowerShell

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing F2 instruction; pending the Tech Lead's review |
| **Date** | 2026-10-31 |
| **Task** | T-F2-50 |
| **REQ** | REQ-WIN-001, REQ-WIN-002, REQ-WIN-003; REQ-PKG-005's executable check |
| **Folded into** | API 1.59 (header, §2), Tech 1.85 (§5.3i, §9.3), constitution 1.6 (Stack row) |

## Evidence

- `GOOS=windows CGO_ENABLED=0 go build ./...` fails today in ten files. The failures are not
  incidental: each one is a Unix guarantee the specs rely on.
  - `config/paths.go` checks that the runtime directory is owned by the user, from `Stat_t.Uid`.
  - `config/instancelock.go` takes the instance lock with `flock`.
  - `context/adapters/skills/bundle.go` refuses hardlinks from `Stat_t.Nlink`, and symlinks and
    FIFOs with `O_NOFOLLOW|O_NONBLOCK`.
  - `sessions/adapters/pty` is `creack/pty`, which has no ConPTY.
  - `handoff` hands descriptors over with `SCM_RIGHTS`.
  - `acp/adapters/sdk` ends a hosted agent's tree through its POSIX session.
  - `remote` checks a pid with `kill(pid, 0)`.
  - `go.mitchellh.com/libghostty` is cgo; without libghostty-vt built for Windows nothing that
    renders a terminal links.
- The constitution's Stack row already names `aymanbagabas/go-pty` for ConPTY and "Unix socket /
  named pipe" for the protocol. `zalando/go-keyring`, already a dependency, reads the Windows
  Credential Manager.
- No machine here runs Windows. Every Windows claim below is proved on GitHub's `windows-latest`
  runner, not by reading.

## Decisions

1. **Target.** Windows 11 x86_64 (`windows/amd64`), cgo through MinGW-w64, libghostty-vt built
   by the pinned Zig (`task deps:ghostty` on Windows) and linked as `ghostty-vt.dll`, built with
   `-tags dynamic` and shipped beside the binaries: Zig's static archive leaves compiler-rt
   (`__chkstk`) and `ntdll` to Zig's own linker, which MinGW's `ld` does not supply.
2. **Paths.** Runtime `%LOCALAPPDATA%\Umbral\run`, data `%LOCALAPPDATA%\Umbral\data`, config
   `%APPDATA%\Umbral`. A set `XDG_*` variable still wins, as on Linux, which is what keeps the
   tests hermetic. The runtime directory's private check reads its owner and DACL: owned by the
   current user's SID, protected, and granting access to that SID only. Anything else is refused,
   as a wrong mode is on Unix.
3. **Transport.** The protocol runs over the named pipe `\\.\pipe\umbral-<16 hex of SHA-256 of
   the runtime directory>`, so two runtime directories never share a pipe. Its security
   descriptor is `D:P(A;;GA;;;<user SID>)`: the current user only (REQ-WIN-001). The token file
   stays in the runtime directory. A client dials the same name from the same directory. The
   name is predictable, so another account could create the pipe first: the daemon then fails to
   start, because its first instance is created exclusive (`FILE_FLAG_FIRST_PIPE_INSTANCE`), and
   a client checks that the pipe's server process (`GetNamedPipeServerProcessId`) runs as the
   current user before it writes anything, so the token never reaches an impostor. The
   pipe library is `Microsoft/go-winio`; the constitution's Stack row gains it.
4. **Peer.** The daemon reads a client's pid with `GetNamedPipeClientProcessId`. Windows has no
   POSIX session, so the agent's-terminal check (REQ-ORC-004) walks that pid's ancestors from a
   Toolhelp snapshot, dropping a parent link whose parent was created after its child (a reused
   pid), and asks in place of the session whether the pid runs in an agent's Job Object: every
   session's shell runs in one (decision 5) and every hosted agent (decision 10), so a process an
   agent's children left behind is still the agent's. A process the check cannot ask about is
   taken for an agent's while the job is open. Detection (REQ-XAG-001) reads a process's
   executable, since its command line lives in its own memory.
5. **PTY.** ConPTY through `aymanbagabas/go-pty`, behind the existing `sessions/ports` PTY port.
   The emulator, blocks and snapshots are unchanged. The shell runs in a Job Object that kills its
   processes when closed, Windows' process group: a hang-up closes the pseudo console
   (CTRL_CLOSE_EVENT), a kill closes the job, the agent's cancel types Ctrl-C (CTRL_C_EVENT) and
   its second half terminates the shell's descendants. The daemon clears its inherited "ignore
   Ctrl-C" flag before its first shell, or a daemon started detached would hand it on. A shell is a
   file whose extension `PATHEXT` runs, and a command name resolves through the child's
   `PATHEXT`; `Path` is `PATH`.
6. **Shell.** PowerShell 7 (`pwsh`) is the default shell on Windows when `$SHELL` is unset, then
   `%ComSpec%`, with a bootstrap dot-sourced by `-NoExit -Command` after the user's profiles: it
   emits OSC 133 `A`/`B` and OSC 7 from its prompt function, `633;E` and `C` from
   `PSConsoleHostReadLine` once Enter returns a non-empty line, and `D` with a native command's
   exit code, or 1 for a failed cmdlet. This is REQ-BLK-008, a MUST on Windows (REQ-WIN-002). `cmd.exe` and Windows
   PowerShell 5.1 run without integration, `integration: none`.
7. **Secrets.** The keyring is the Windows Credential Manager through `go-keyring` (REQ-WIN-003).
   The environment fallback of Art. 5 is unchanged.
8. **Instance lock.** `LockFileEx` on the same lock files.
9. **Skill store.** A bundle entry is refused when it is a reparse point (symlink or junction),
   or when `GetFileInformationByHandle` reports more than one link. Entries are opened with
   `FILE_FLAG_OPEN_REPARSE_POINT`, so a swapped entry is not followed.
10. **Hosted ACP agents.** A hosted agent starts suspended, joins a Job Object with
    `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, and is resumed, so nothing it starts escapes the job;
    ending its turn or its exit closes the job, which ends every process it left. The job is
    listed from the moment the agent joins it, before any handshake, and the peer check asks it
    (decision 4).
11. **Not on Windows in T-F2-50, each refused in words rather than half done:**
    - `umbrald --takeover` (REQ-HND-001) exits `EX_UNAVAILABLE` with "live handoff is not
      supported on Windows". ConPTY handles can be duplicated into another process, so this is a
      later task, not a design limit.
    - Remote hosts (REQ-RMT-*): `remote.add` answers `NOT_IMPLEMENTED` on Windows, since Windows
      OpenSSH's stream-local forwarding is not reliable enough to promise REQ-RMT-001.
12. **Desktop.** `cmd/umbral-desktop` is built for Windows in CI, and the check REQ-PKG-005 left
    open reads the executable's icon group against the kit's `.ico`.
13. **CI.** A `windows` job on `windows-latest` builds every binary, runs the packages that
    support Windows with the race detector, the VT conformance suite's MUST cases and the block
    tests under `pwsh` (REQ-WIN-002), a named-pipe round trip that a second user's token cannot
    open, and a Credential Manager round trip.

14. **Commands ask on Windows.** The destructive floor parses POSIX shell lines
    (`mvdan.cc/sh/v3/syntax`, delta `2026-10-shell-parser`); an agent's shell on Windows is
    PowerShell or cmd, whose lines it cannot read. Every non-empty line therefore asks there,
    reported by the destructive step as `windows_shell`: full-auto does not run it, an allow rule
    does not carry it, and it is never remembered `always`. A deny rule still refuses. Keys sent to a
    running program are read the same way: delta `2026-10-terminal-interact` (decision 4) also
    reads them as the shell's next line, in case the program does not take them, and that shell's
    lines are not read here, so a line of program input asks too. A parser for
    PowerShell lines is later work.
15. **Paths compare as NTFS does.** Deny and allow rules over paths match a target without case and
    with `/` read as `\`, as the file system opens it; otherwise `c:/SECRETS/key` would step around
    a deny of `C:\secrets\*`. The write root is compared the same way (`filepath.Rel` is
    case-insensitive on Windows). Short names and links are the callers' to resolve first, as
    symlinks already are.
16. **The daemon's last words.** Closing a pipe's server end discards what the client has not read,
    so a connection's close flushes for up to a second on a goroutine of its own before closing:
    the UNAUTHORIZED or VALIDATION_ERROR sent before a hang-up arrives, and a client that stopped
    reading cannot hold the close (REQ-API-006).

## Layers

T-F2-50 lands as one PR, built in this order. Each layer is green on the Windows job before the
next starts:

1. Build seams: every Unix-only call behind a `_unix.go`/`_windows.go` pair, the Windows side
   refusing (`ErrUnsupported`) where its layer has not landed; `GOOS=windows CGO_ENABLED=0 go vet`
   over the non-cgo packages in `task ci`.
2. Paths, the private-directory check, the instance lock and the named pipe; the API package's
   tests on the Windows job.
3. libghostty-vt for Windows, ConPTY and the PowerShell bootstrap; the conformance suite and block
   tests under `pwsh`.
4. Peer check, Credential Manager, skill store, hosted ACP agents.
5. The desktop build and its icon check.
