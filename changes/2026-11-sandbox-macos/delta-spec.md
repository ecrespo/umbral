# Delta — the macOS sandbox adapter

| Field | Value |
|---|---|
| **Status** | `PROPOSED`. Written with its code, not before: the Tech Lead asked for T-HRD-16 on 2026-11-19 in one instruction with the tasks around it. It is the Tech Lead's to ratify |
| **Date** | 2026-11-19 |
| **Task** | T-HRD-16 |
| **REQ** | amends REQ-SBX-001 (the socket clause); no new requirement |
| **Raised by** | Delta `2026-11-agent-sandbox` decision 3; delta `2026-11-sandbox-linux`, whose port and profile this adapter serves; `docs/spikes/2026-11-seatbelt.md` |
| **Depends on** | T-HRD-15 |

## Evidence

`docs/spikes/2026-11-seatbelt.md`, measured on the `macos-latest` runner — there is no other
Mac. What shaped the decisions:

- `sandbox-exec` with a generated profile refuses writes, reads **and `stat`** by path, for the
  command and everything it starts.
- A profile written with an unresolved path (`/var/…` for `/private/var/…`) matches nothing.
- Refusing every connection to a local socket stops name resolution; allowing the resolver's
  socket back restores it.
- launchd, Launch Services and System Events ran a command outside the sandbox for an
  unconfined caller and refused a confined one.
- A confined process can signal any process of the user's unless signals are refused and
  allowed again for its own sandbox only.
- Every Mach lookup can be refused without breaking a shell, git, a name lookup or an HTTPS
  request; with them open, a confined process wrote a preference through the user's
  preferences daemon.
- A hard link, a clone and a rename of a denied file are refused to the confined process, and
  so is another spelling of its path on a file system that ignores case.

## Decisions

1. **`internal/sandbox/adapters/seatbelt`** implements the same port. `cmd/umbrald` picks it on
   macOS and Landlock elsewhere. No helper: Apple's tool applies the profile before the
   command runs. It is named by its full path, `/usr/bin/sandbox-exec`.
2. **The profile is the one T-HRD-15 makes**, rendered as rules. The last rule that matches
   decides, so each refusal comes before what it leaves open:
   - every write refused, then allowed under each write entry and on the device files;
   - every read refused under each denied directory, then allowed again under a write entry
     inside one — the thread's worktree and root. Each directory above such an entry, up to
     the denied one, answers `stat` and nothing else, because resolving a path asks it.
3. **Local sockets.** A connection to another process's Unix-domain socket is refused, the
   resolver's excepted. A socket pair, and a socket the agent binds in its own directory,
   work — unlike Linux, where the filter refuses the socket itself. REQ-SBX-001's clause
   becomes "a connection to another process's Unix-domain socket", which both adapters meet.
4. **Launch Services, Apple events and launchd jobs are refused in the profile**, though
   launchd already refused a confined caller when measured: the profile should not depend on
   that.
5. **Mach services.** Every lookup is refused, and thirteen are allowed back: logging, the
   directory service, name resolution and certificate trust. The preferences daemon, the
   keychain, Launch Services and the rest of the user's session are not among them. Found by
   the security review of this task's first commit, which had left every lookup open.
6. **Signals** reach only a process in the same sandbox: the agent's own children. The
   review's finding too.
7. **`enforcing` is probed, not assumed.** At start the adapter runs the tool once under a
   profile that allows everything; where that fails — the tool missing, the daemon itself
   inside a sandbox — the status is `unavailable`, `seatbelt_unavailable`.
8. **A path is data.** Quotes and backslashes are escaped; a path with a control character
   is refused, and so is a command whose name the tool would read as one of its flags.
9. **What this adapter does not close**, beyond decision 15 of `2026-11-sandbox-linux`:
   - the name of a denied directory shows in its parent's listing;
   - a denied file the **user** hard-linked elsewhere is read;
   - whatever the thirteen services allowed back can be made to do for a caller;
   - `sandbox-exec` is deprecated by Apple. It is what other agent tools use; the adapter is
     small and behind a port.
   Better than Linux: `stat`, `chmod`, `chown`, times and extended attributes are refused by
   path, and the keychain is out of reach.
10. **What it may break.** A program that needs a session service outside the thirteen — a
    credential helper that reads the keychain, a tool that opens a browser — fails under the
    profile, and so does one that sandboxes its own commands, since a sandbox cannot be
    applied inside one. `[sandbox] mode = "off"` is the way out, disclosed.

## Amendments

- **REQ-SBX-001**: "and refuses it a Unix-domain socket" becomes "and refuses it a connection
  to another process's Unix-domain socket".

## Spec changes

- PRD F2 0.18 §6.13: REQ-SBX-001's socket clause.
- API 1.64 §5.1: `mechanism` may be `seatbelt`; `reason` may be `seatbelt_unavailable`.
- Tech 1.99 §5.3f: the macOS adapter.

## Open questions

- **The thirteen services.** They were chosen by what they are for, not by measuring a real
  agent: none was needed by anything the spike ran. Recommended: keep them until an agent is
  run on a Mac, then trim or extend.
- **A stricter base.** `(deny default)` with an allow list is what Apple's own profiles use
  and would close the Mach services. It cannot be written without a Mac to measure what a
  real agent needs. Recommended: later, with one.

## Verification

- The renderer, on any system: the order of the rules, the two reopened directories and
  their ancestors' metadata, the socket rule and the resolver's exception, the three
  refusals, quoting, and the command line. Each rule was removed in turn and a test went red.
- Against the kernel, on `macos-latest` only: T-HRD-15's cases through this adapter — writes
  refused outside the profile and landing inside, the device files, Umbral's directories
  unreadable and the thread's own readable, another process's socket unreachable, a launchd
  job and a Launch Services open refused, a signal to a process outside refused and one to
  its own child delivered, a preference not written, a hard link, a clone and another
  spelling of a denied file refused — and the hosted-agent tests of `cmd/umbrald`
  against a real daemon. CI's macOS job now fails if the runner cannot confine.
- Not verified: a Mac with a desktop session, an Intel Mac, a real hosted agent, and each
  kernel rule's mutation — the kernel tests cannot be run where the code was written.
