# Delta — Windows: ConPTY, a named pipe, Credential Manager and PowerShell

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-10-31`; self-approved by the implementing agent under the Tech Lead's standing F2 instruction once its first layer lands |
| **Date** | 2026-10-31 |
| **Task** | T-F2-50 |
| **REQ** | REQ-WIN-001, REQ-WIN-002, REQ-WIN-003; REQ-PKG-005's executable check |
| **Folded into** | (when approved) API §1 transport, Tech §5.1 paths, §5.3 (peer check), §9 (Windows build), constitution's Stack row (named pipe library) |

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
   by the pinned Zig for `x86_64-windows-gnu` (`task deps:ghostty` on Windows).
2. **Paths.** Runtime `%LOCALAPPDATA%\Umbral\run`, data `%LOCALAPPDATA%\Umbral\data`, config
   `%APPDATA%\Umbral`. A set `XDG_*` variable still wins, as on Linux, which is what keeps the
   tests hermetic. The runtime directory's private check reads its owner and DACL: owned by the
   current user's SID, protected, and granting access to that SID only. Anything else is refused,
   as a wrong mode is on Unix.
3. **Transport.** The protocol runs over the named pipe `\\.\pipe\umbral-<16 hex of SHA-256 of
   the runtime directory>`, so two runtime directories never share a pipe. Its security
   descriptor is `D:P(A;;GA;;;<user SID>)`: the current user only (REQ-WIN-001). The token file
   stays in the runtime directory. A client dials the same name from the same directory. The
   pipe library is `Microsoft/go-winio`; the constitution's Stack row gains it.
4. **Peer.** The daemon reads a client's pid with `GetNamedPipeClientProcessId`. Windows has no
   POSIX session, so the agent's-terminal check (REQ-ORC-004) walks that pid's ancestors from a
   Toolhelp snapshot and nothing else. A pid whose parent was reused is the known limit of that
   walk; the daemon stops at the first ancestor created after its child.
5. **PTY.** ConPTY through `aymanbagabas/go-pty`, behind the existing `sessions/ports` PTY port.
   The emulator, blocks and snapshots are unchanged.
6. **Shell.** PowerShell 7 (`pwsh`) is the default shell on Windows, with a bootstrap that emits
   OSC 133 `A`/`B` from its prompt function, `C` from a PSReadLine Enter handler and `D` with the
   last exit code. This is REQ-BLK-008, a MUST on Windows (REQ-WIN-002). `cmd.exe` and Windows
   PowerShell 5.1 run without integration, `integration: none`.
7. **Secrets.** The keyring is the Windows Credential Manager through `go-keyring` (REQ-WIN-003).
   The environment fallback of Art. 5 is unchanged.
8. **Instance lock.** `LockFileEx` on the same lock files.
9. **Skill store.** A bundle entry is refused when it is a reparse point (symlink or junction),
   or when `GetFileInformationByHandle` reports more than one link. Entries are opened with
   `FILE_FLAG_OPEN_REPARSE_POINT`, so a swapped entry is not followed.
10. **Hosted ACP agents.** A hosted agent runs in a Job Object with
    `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; ending its turn or its exit closes the job, which ends
    every process it left. The peer check counts the job's processes as the agent's.
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
