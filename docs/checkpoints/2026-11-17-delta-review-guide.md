# Review guide — the 16 open deltas and T-HRD-01

> 2026-11-17 · for the Tech Lead · written from the deltas' own text on `develop` at `bf9092f`.
> It is a reading order and a list of points to look at, not a verdict: ratifying, amending or
> refusing each delta is the Tech Lead's (findings 1 and 2 of
> `docs/checkpoints/2026-11-17-repo-review.md`).

## How a delta closes

As the T-HRD-06 … 10 deltas closed (`0c3f56d`): the status line becomes
`RATIFIED <date> by the Tech Lead ("<the words>")`, the directory moves to `changes/_archive/`,
and `specs/README.md` and `AGENTS.md` take the new counts. A delta that is amended gets the
amendment in its text first. Fifteen of these were approved by the implementing agent; twelve
say "pending the Tech Lead's review" and three (`pty-handoff`, `remote-hosts`,
`client-reconnect`) do not say it but have no ratification either.

## Reading order

Ordered by how much a mistake would cost, not by date.

### 1. `2026-10-windows-port` (T-F2-50) — a second platform's whole trust boundary

- **Decision 3:** the pipe's name is predictable. The defence is the exclusive first instance
  plus the client checking the server process's user before it writes the token. Both are read
  back in CI; neither is exercised against a second account, because the runner has one
  (decision 17).
- **Decision 4:** the agent's-terminal check walks a Toolhelp snapshot and Job Objects in place
  of the POSIX session. "A process the check cannot ask about is taken for an agent's" — the
  safe direction; confirm it is the one wanted for a user's own elevated tools.
- **Decision 14:** every command line asks on Windows; `full-auto` runs none. Ratifying this
  ratifies a Windows agent that is far more interruptive than the Unix one until a PowerShell
  parser exists.
- **Decision 17's limits to accept or refuse:** the Job Object is assigned just after the shell
  starts; 8.3 names, alternate data streams and `\\?\` paths are spellings a path rule does not
  see; `windows-latest` is Windows Server, so Windows 11 itself is untested.

### 2. `2026-10-remote-hosts` (T-F2-41) — another machine's daemon behind this one's checks

- **The host key is not mentioned.** `BatchMode=yes` makes an unknown host fail instead of
  prompting under ssh's defaults, but the delta sets no `StrictHostKeyChecking`, so a user
  config with `StrictHostKeyChecking no` applies to the link that carries the host's token.
  Worth one sentence in the delta either way.
- **Decision 1:** a `LocalForward`, `RemoteForward` or `DynamicForward` in the user's ssh config
  still applies to the forward; REQ-RMT-001's "no TCP port" is the daemon's promise only.
- **Decision 6:** the `userOnly` list that protects a host through the relay is the *relaying*
  daemon's, because the host sees sshd as its peer. A host on a newer build is protected by the
  older list.
- **Not done:** a slow host can hold a client connection's other requests for up to 15 s; nested
  prefixes (`a/b/w1`) and the desktop's resubscription have no test.

### 3. `2026-10-pty-handoff` (T-F2-40) — every session's master crosses a socket

- **Decision 2:** `handoff.sock` refuses another uid and an agent's terminal. The uid check
  "needs a second user and is not tested" (Tests).
- **Decision 4:** after a failed takeover whose migrations already ran, the old daemon keeps
  serving on the newer schema. That leans on every future migration being additive (Art. 6);
  the delta is where that dependency is written down.
- **Decision 8:** a test-only failure injection reached through `-ldflags -X`. Confirm a
  release build's variable is empty — the release workflow builds without it.
- **Decision 5:** the old daemon's MCP supervisors may write a server's stored state after the
  new daemon has.

### 4. `2026-10-plugins` (T-F2-60) — third-party content that starts processes

- **Decisions 4 and 5:** a plugin's `stdio` MCP server starts as soon as it is installed, with
  its secret references resolved; `untrusted` limits what the agent may do with its tools, not
  whether the process runs. The confirmation shows every reference. An interrupted install's
  server runs once more at the next start before it is removed.
- **Decision 6:** the reading of REQ-PLG-002 — a plugin's command is decided when an agent
  reaches it and not when the user launches it — was folded into PRD F2 0.14 by this delta. It
  is a change to a requirement's meaning and the part most in need of an explicit yes.
- **Decision 8:** a bound key types a line at the prompt without Enter.
- **Not in this task:** signed or remote plugin sources.

### 5. `2026-10-terminal-interact` (T-F2-20) — the agent types into running programs

- spec-guardian's first pass was FIX FIRST with nine findings, all fixed with a test, and the
  verify pass found two more. The delta was amended on 2026-10-26 after CI.
- The residuals are in Tech Design §5.3: text typed ahead to a program that does not read it, a
  shell that rebinds keys or sits in vi command mode, and a continuation line.
- **Decision 6:** take-over is for the `tui` and `desktop` kinds only.

### 6. `2026-10-desktop-client` (T-F2-30) and `2026-10-hunks-file-tree` (T-F2-31)

- **Desktop decision 2:** the API lets a `desktop` connection call *every* method; the limit to
  `desktop.Methods` is the client's own. The page's CSP is strict, but the daemon does not
  enforce the list.
- **Desktop decision 9:** the page's own logic has no unit test; WebKit's sandbox needs
  unprivileged user namespaces.
- **Hunks decision 8:** `worktree.write_file` is a new write path from a client into a
  worktree, guarded by `expected_sha256`.
- **Hunks residuals:** an interrupted save leaves `.umbral-edit-<nonce>` where a merge would
  land it; an approval carries its diff twice and can approach the 4 MiB frame limit; the page
  is checked by hand.

### 7. `2026-10-yzma-provider` (T-F2-21) and `2026-10-active-ai` (T-F2-22)

- **yzma decisions 2 and 7:** llama.cpp's shared libraries are loaded into the daemon from
  `lib_path`; a crash inside them ends the daemon and every pane. A child process is named as
  the alternative and deferred.
- **Active AI decision 9:** "local" is a loopback `base_url`; a loopback gateway that forwards
  to the cloud counts as local. The mitigation is a sentence in `docs/user/providers.md`.
- **Active AI decision 8:** the suggestion is stripped of escapes and its attachment tokens made
  inert.

### 8. `2026-10-usf2-002-live` (T-F2-23) — needs a decision, not only a ratification

- 0 of 10 runs green against a 70 % threshold: psql 10/10, `git rebase -i` 0/10, every failure
  the model's use of vim. The daemon's properties held in every run.
- **To decide:** whether US-F2-002 passes, and so whether slice F2b closes. The options the
  delta leaves: accept on the daemon's properties as F2a was accepted; change the scenario or
  give the agent a non-interactive `GIT_SEQUENCE_EDITOR`; or measure another model.
- **Residuals:** an ESC prefix inside text is refused; the model does not track vim's cursor.

### 9. The rest — low risk, read for completeness

| Delta | Task | Point to look at |
|---|---|---|
| `2026-10-service-units` | T-PKG-06, 07 | No live handoff under systemd; the launchd reasoning is "reasoned from both, not measured on macOS"; macOS archives unsigned in 0.1 |
| `2026-10-opencode-fixed-env` | T-HRD-11 | Not measured live; the npm provider package stays the agent's own traffic |
| `2026-10-client-reconnect` | T-F2-42 | A redial never autostarts a daemon |
| `2026-10-pane-graphics` | T-F2-61 | A byte-level filter per subscription; a fuzzing candidate |
| `2026-10-icons-render-tolerance` | T-PKG-01 | A pixel tolerance in the CI comparison |
| `2026-10-wails-icons` | T-PKG-04 | Already approved by the Tech Lead; only the archive is missing |

## T-HRD-01 — the rule-signing key

Nobody but the Tech Lead can do this one: the key is created offline and its private half never
touches a repository, a CI secret or an agent's session. The steps are
`docs/runbooks/rule-signing.md` §4.1:

1. On the workstation, network off:
   `rulesign keygen 2> umbral-rules-2026a.pub | age -p -o umbral-rules-2026a.key.age`.
2. Copy the `.key.age` to the primary and the backup drive; `shred -u` the workstation copy.
3. Restore from each drive once: decrypt, sign a scratch file, `rulesign verify`.
4. Open a pull request adding the `.pub` to
   `internal/security/adapters/rules/trustseed/seed.json` under a fixed `key_<ULID>` id, with
   the fingerprint in the description.
5. Publish the fingerprint in the README and the release notes.

T-HRD-01 is done when a bundle signed with the key and served locally is accepted by a daemon
built with the seed, and one signed with any other key is refused and recorded in `egress_log`.
Step 4 is the only part an agent can help with, once the `.pub` exists.
