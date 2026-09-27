# Delta — what context assembly reads, bounds and refuses

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-27 — approved by E. Crespo together with the other three F1 deltas of T-F1-11…T-F1-14, as written, with the optional REQ sharpenings folded. Folded into PRD 1.15, API 1.21, Tech Design 1.29 and Data Model 1.13` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-11 |
| **Raised by** | The `spec-guardian` review of T-F1-11, 2026-09-27 (findings 1, 3, 4 and 5) |

## Evidence

REQ-CTX-001 names the rules files and their precedence; REQ-CTX-002 the attachment kinds;
REQ-CTX-003 "the current branch, `git status --short` and `git diff --stat`"; REQ-CTX-005 caps an
attachment at 256 KiB. They leave open:

1. **Git runs commands a repository names.** `git status` runs `core.fsmonitor`, and `status`
   and `diff` run a filter driver's `clean` command on a file whose stat information changed —
   reproduced. In `auto-edit` the agent may write `.git/config` inside the write root without a
   prompt, so the next turn's context read would run a command that never reached
   `security.Decide()`.
2. **A rules file is read because of where it is.** `git clone` recreates symlinks, so a cloned
   repository can ship `AGENTS.md -> ~/.aws/credentials`, which would enter every system prompt
   with only the redaction backstop between it and a provider.
3. **Outputs with no bound.** A repository with thousands of changes fills the window with
   `git status`; REQ-CTX-005's cap is for attachments, not rules files.
4. **A git that fails.** A timeout or an ownership refusal (`safe.directory`) is not "not a
   repository", and dropping the section silently hides it.

## Decisions

1. **No repository-named command runs.** Git is run with `core.fsmonitor=false`, every filter
   driver the configuration defines overridden (`clean`, `smudge` and `process` empty,
   `required=false`), `--no-ext-diff`, `--no-textconv`, `diff.external` empty,
   `--ignore-submodules=dirty`, no optional locks and no prompt. A filter name that a `-c`
   override cannot carry (it holds `=` or a newline) stops git from reading the worktree, and the
   context says so. Cost: a file an LFS-like filter manages may show as modified.
2. **Rules files stay in the repository.** A candidate is read only if its resolved path is inside
   the resolved write root; a link out of it is skipped, a link within it is followed. A rules file
   that is not a regular file, or holds a NUL byte, is skipped.
3. **Bounds.** Rules files are capped like attachments (256 KiB, the omitted bytes stated); each
   git section at 32 KiB, cut at a line, the omitted bytes stated; each git command 5 s.
4. **A failing git is stated.** "Not a git repository", or no git installed, means no git
   section. Any other failure — a timeout, a refusal — gives a section saying the state could not
   be read and why. A cancelled turn is an error.
5. **Attachments:** a `dir` is a one-level listing; binary content (a NUL byte) is never inlined,
   and the context says how many bytes were left out; `stdin` arrives with `umb ai` (T-F1-19) and
   is refused until then.

## Known gaps, accepted

- **Check to use:** a rules file is resolved, checked inside the root, then opened by its
  resolved path; an intermediate directory swapped for a link in between could redirect the read.
  The filter-driver list is read with `git config`, then `status` and `diff` run; a `.git/config`
  written in between escapes the overrides. Both need a writer racing the turn's context read.

## Impact

- Tech §5.3c (1.25) states decisions 1–5.
- REQ-CTX-003 could gain "bounded, and without running commands the repository's configuration
  names" if the Tech Lead wants the PRD to say it outright.
- No API or Data Model change.
